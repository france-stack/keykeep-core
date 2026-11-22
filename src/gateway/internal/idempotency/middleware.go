package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/keykeep/gateway/internal/httpmw"
	"github.com/keykeep/gateway/internal/reqbody"
	"github.com/keykeep/gateway/internal/server"
	"github.com/keykeep/gateway/internal/virtualkey"
)

const (
	requestIDHeader = "X-Request-Id"
	maxRequestID    = 128
)

// Auditor 是审计旁路；nil 表示不写审计。契约收敛在 internal/httpmw（C17）。
type Auditor = httpmw.Auditor

// ClaimStore 是声明存储接口；Store 为 PostgreSQL 实现，测试可注入 fake。
type ClaimStore interface {
	Acquire(ctx context.Context, c Claim) (claimed *Claim, existing *Claim, err error)
	Finalize(ctx context.Context, id, status string, statusCode int, errorCode string) error
}

// Middleware 在虚拟 Key 校验之后、白名单/限流/预算/转发之前执行。
func Middleware(store ClaimStore, auditor Auditor, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodPatch {
			next.ServeHTTP(w, r)
			return
		}
		info, ok := virtualkey.FromContext(r.Context())
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		requestID := strings.TrimSpace(r.Header.Get(requestIDHeader))
		if requestID == "" {
			next.ServeHTTP(w, r)
			return
		}
		if len(requestID) > maxRequestID {
			writeError(w, http.StatusBadRequest, "invalid_request_id")
			return
		}

		body, tooLarge := reqbody.Read(r)
		if tooLarge {
			// 超过 8 MiB 的请求不做幂等（不读全量正文，避免内存放大）。
			next.ServeHTTP(w, r)
			return
		}
		// 声明里的 model/stream 来自链级共享摘要；哈希仍用原始字节，不受摘要口径影响。
		hint := reqbody.BodyHint(r)
		sum := sha256.Sum256([]byte(r.Method + "\n" + r.URL.Path + "\n" + string(body)))

		claim := Claim{
			KeyID:       info.ID,
			EmpID:       info.EmpID,
			RequestID:   requestID,
			PayloadHash: hex.EncodeToString(sum[:]),
			Path:        r.URL.Path,
			Model:       hint.Model,
			Stream:      hint.Stream,
		}
		claimed, existing, err := store.Acquire(r.Context(), claim)
		if err != nil {
			log.Printf("idempotency: acquire: %v", err)
			writeError(w, http.StatusServiceUnavailable, "idempotency_unavailable")
			return
		}
		if existing != nil {
			rejectDuplicate(w, r, auditor, info, existing, claim.PayloadHash)
			return
		}

		state := &server.RequestState{}
		recorder := httpmw.NewRecorder(w)
		ctx := server.WithRequestState(r.Context(), state)
		defer func() {
			recovered := recover()
			finishCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Second)
			finalize(finishCtx, store, recorder, state, recovered != nil, r, claimed.ID)
			cancel()
			if recovered != nil {
				panic(recovered)
			}
		}()
		next.ServeHTTP(recorder, r.WithContext(ctx))
	})
}

func rejectDuplicate(w http.ResponseWriter, r *http.Request, auditor Auditor, info *virtualkey.KeyInfo, existing *Claim, payloadHash string) {
	code := "outcome_unknown"
	reason := existing.Status
	switch {
	case existing.PayloadHash != payloadHash:
		code = "request_id_conflict"
	case existing.Status == StatusInProgress:
		code = "request_in_progress"
	case existing.Status == StatusCompleted:
		code = "request_already_completed"
	case existing.Status == StatusFailed:
		code = "request_already_failed"
	}
	log.Printf("idempotency: duplicate key=%s request=%s state=%s code=%s", info.ID, existing.RequestID, existing.Status, code)
	if auditor != nil {
		auditor.Record(r.Context(), "request.duplicate", info.EmpID, map[string]any{
			"keyId":     info.ID,
			"requestId": existing.RequestID,
			"state":     existing.Status,
			"code":      code,
		}, existing.RequestID)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
		"code":       code,
		"requestId":  existing.RequestID,
		"state":      existing.Status,
		"statusCode": existing.StatusCode,
		"reason":     reason,
	}})
}

// finalize 依据代理状态与响应码写终态；status 为空表示删除声明。
func finalize(ctx context.Context, store ClaimStore, recorder *httpmw.Recorder, state *server.RequestState, panicked bool, r *http.Request, claimID string) {
	status := recorder.Status()
	gotResponse, upstreamStatus, proxyErr := state.Result()

	finalStatus, statusCode, errorCode := "", 0, ""
	switch {
	case panicked:
		finalStatus, errorCode = StatusOutcomeUnknown, "stream_aborted"
	case gotResponse:
		statusCode = upstreamStatus
		if upstreamStatus >= 200 && upstreamStatus < 300 {
			finalStatus = StatusCompleted
		} else {
			finalStatus, errorCode = StatusFailed, "upstream_status"
		}
	case proxyErr != nil:
		statusCode = status
		finalStatus = proxyErrorOutcome(proxyErr)
		errorCode = "proxy_error"
	case status == http.StatusPaymentRequired || status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable:
		// 网关自身拒绝（预算/限流/并发），允许窗口过后用同一 id 重试。
		finalStatus = ""
	case r.Context().Err() != nil:
		statusCode = status
		finalStatus, errorCode = StatusOutcomeUnknown, "client_canceled"
	case status >= 200 && status < 400:
		statusCode = status
		finalStatus = StatusCompleted
	case status == 0:
		finalStatus, errorCode = StatusOutcomeUnknown, "no_response"
	default:
		statusCode = status
		finalStatus, errorCode = StatusFailed, "gateway_rejected"
	}

	if err := store.Finalize(ctx, claimID, finalStatus, statusCode, errorCode); err != nil {
		log.Printf("idempotency: finalize claim %s (%s): %v", claimID, finalStatus, err)
	}
}

func proxyErrorOutcome(err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return StatusOutcomeUnknown
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return StatusOutcomeUnknown
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return StatusFailed // 连接都没建立，肯定没执行
	}
	return StatusOutcomeUnknown // 可能已发出，禁止自动重试
}

func writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code}})
}

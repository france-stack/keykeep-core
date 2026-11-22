// Package selfservice 提供员工用虚拟 Key 查询自己用量/限额/预算的数据面接口。
//
// 契约见 dev-spec §1.1/§1.5：/v1/models、/v1/limit、/v1/budget、/v1/usage/*。
// 这些接口在网关本地处理，不经过转发中间件（不占限流/并发/预算额度）。
package selfservice

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keykeep/gateway/internal/policy"
	"github.com/keykeep/gateway/internal/ratelimit"
	"github.com/keykeep/gateway/internal/virtualkey"
)

// Service 处理自助查询；所有查询都以 KeyInfo 的 empId 为边界。
type Service struct {
	pool    *pgxpool.Pool
	counter ratelimit.Counter
	limits  *ratelimit.Store
	now     func() time.Time
}

func New(pool *pgxpool.Pool, counter ratelimit.Counter, limits *ratelimit.Store) *Service {
	return &Service{pool: pool, counter: counter, limits: limits, now: time.Now}
}

// Paths 是自助接口前缀；其余路径交给转发层。
func Paths() []string {
	return []string{"/v1/models", "/v1/limit", "/v1/budget", "/v1/usage/"}
}

// Handles 判断路径是否属于自助接口。
func Handles(path string) bool {
	for _, prefix := range Paths() {
		if path == prefix || strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	info, ok := virtualkey.FromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid_api_key")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}

	switch {
	case r.URL.Path == "/v1/models":
		s.handleModels(w, r, info)
	case r.URL.Path == "/v1/limit":
		s.handleLimit(w, r, info)
	case r.URL.Path == "/v1/budget":
		s.handleBudget(w, r, info)
	case r.URL.Path == "/v1/usage/requests":
		s.handleUsageRequests(w, r, info)
	case r.URL.Path == "/v1/usage/summary":
		s.handleUsageSummary(w, r, info)
	default:
		writeError(w, http.StatusNotFound, "not_found")
	}
}

// ---------------------------------------------------------------- models

type modelRow struct {
	Alias    string
	Provider string
	Created  int64
}

func (s *Service) handleModels(w http.ResponseWriter, r *http.Request, info *virtualkey.KeyInfo) {
	rows, err := s.pool.Query(r.Context(),
		`SELECT DISTINCT m.alias, c."providerType", (extract(epoch from c."createdAt"))::bigint
		   FROM "ModelMap" m
		   JOIN "ProviderCredential" c ON c.id = m."credentialId"
		  WHERE c.disabled = false
		  ORDER BY m.alias`)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "model_store_unavailable")
		return
	}
	defer rows.Close()

	// 同一别名可以挂多条凭据（渠道组）；对外只暴露一次，取最早的创建时间。
	seen := make(map[string]struct{})
	data := make([]map[string]any, 0)
	for rows.Next() {
		var row modelRow
		if err := rows.Scan(&row.Alias, &row.Provider, &row.Created); err != nil {
			writeError(w, http.StatusServiceUnavailable, "model_store_unavailable")
			return
		}
		if len(info.AllowedModels) > 0 && !policy.MatchesAny(info.AllowedModels, row.Alias) {
			continue
		}
		if _, exists := seen[row.Alias]; exists {
			continue
		}
		seen[row.Alias] = struct{}{}
		data = append(data, map[string]any{
			"id":       row.Alias,
			"object":   "model",
			"created":  row.Created,
			"owned_by": row.Provider,
		})
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusServiceUnavailable, "model_store_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

// ---------------------------------------------------------------- limit

type dimView struct {
	Used  int64  `json:"used"`
	Limit int64  `json:"limit"`
	Tier  string `json:"tier"`
}

func (s *Service) handleLimit(w http.ResponseWriter, r *http.Request, info *virtualkey.KeyInfo) {
	keyCfg, err := s.limits.Get(r.Context(), info.ID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "limit_store_unavailable")
		return
	}
	globalCfg, err := s.limits.GetGlobal(r.Context())
	if err != nil {
		globalCfg = ratelimit.Config{}
	}

	keyRPM, keyIn, keyOut, _ := s.counter.Snapshot(r.Context(), info.ID)
	globalRPM, globalIn, globalOut, _ := s.counter.Snapshot(r.Context(), ratelimit.GlobalCounterID)

	dims := map[string]dimView{}
	add := func(name string, keyLimit, globalLimit, keyUsed, globalUsed int64) {
		if keyLimit <= 0 && globalLimit <= 0 {
			return
		}
		// 两级取更紧的一级：限额更小者优先；只配一级时用它。
		limit, tier, used := keyLimit, "key", keyUsed
		if globalLimit > 0 && (keyLimit == 0 || globalLimit < keyLimit) {
			limit, tier, used = globalLimit, "global", globalUsed
		}
		dims[name] = dimView{Used: used, Limit: limit, Tier: tier}
	}
	add("rpm", keyCfg.RPM, globalCfg.RPM, keyRPM, globalRPM)
	add("tpm", keyCfg.TPM, globalCfg.TPM, keyIn+keyOut, globalIn+globalOut)
	add("itpm", keyCfg.ITPM, globalCfg.ITPM, keyIn, globalIn)
	add("otpm", keyCfg.OTPM, globalCfg.OTPM, keyOut, globalOut)

	limited := false
	for _, dim := range dims {
		if dim.Used >= dim.Limit {
			limited = true
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"key":     info.Prefix4 + "…" + info.FP6,
		"limited": limited,
		"dim":     dims,
	})
}

// ---------------------------------------------------------------- budget

type budgetRow struct {
	Scope         string
	LimitCents    int64
	UsedCents     int64
	ReservedCents int64
}

// budgetLevel 按剩余比例给出水位：blocked ≤0，warn ≤20%，其余 ok。
func budgetLevel(limit, used, reserved int64) string {
	remaining := limit - used - reserved
	if limit <= 0 {
		return "ok"
	}
	if remaining <= 0 {
		return "blocked"
	}
	if remaining*5 <= limit {
		return "warn"
	}
	return "ok"
}

func (s *Service) handleBudget(w http.ResponseWriter, r *http.Request, info *virtualkey.KeyInfo) {
	rows, err := s.pool.Query(r.Context(),
		`SELECT scope, "limitCents", "usedCents", "reservedCents"
		   FROM "Budget"
		  WHERE period = 'monthly'
		    AND ((scope = 'emp' AND "scopeId" = $1)
		      OR scope = 'global')`,
		info.EmpID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "budget_store_unavailable")
		return
	}
	defer rows.Close()

	var tightest *budgetRow
	for rows.Next() {
		var row budgetRow
		if err := rows.Scan(&row.Scope, &row.LimitCents, &row.UsedCents, &row.ReservedCents); err != nil {
			writeError(w, http.StatusServiceUnavailable, "budget_store_unavailable")
			return
		}
		if tightest == nil || row.LimitCents-row.UsedCents-row.ReservedCents <
			tightest.LimitCents-tightest.UsedCents-tightest.ReservedCents {
			copied := row
			tightest = &copied
		}
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusServiceUnavailable, "budget_store_unavailable")
		return
	}

	if tightest == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"scope": "none", "period": s.now().Format("2006-01"),
			"limit_cents": 0, "used_cents": 0, "reserved_cents": 0, "level": "ok",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"scope":          tightest.Scope,
		"period":         s.now().Format("2006-01"),
		"limit_cents":    tightest.LimitCents,
		"used_cents":     tightest.UsedCents,
		"reserved_cents": tightest.ReservedCents,
		"level":          budgetLevel(tightest.LimitCents, tightest.UsedCents, tightest.ReservedCents),
	})
}

// ---------------------------------------------------------------- usage

func (s *Service) handleUsageRequests(w http.ResponseWriter, r *http.Request, info *virtualkey.KeyInfo) {
	params := r.URL.Query()
	pageSize := atoiDefault(params.Get("pageSize"), 20)
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	// page 上限随 pageSize 收敛，防止 (page-1)*pageSize 溢出成负 OFFSET 打挂查询。
	page := minInt(maxInt(1, atoiDefault(params.Get("page"), 1)), math.MaxInt/pageSize)

	where := "WHERE r.\"empId\" = $1"
	args := []any{info.EmpID}
	add := func(clause string, value any) {
		args = append(args, value)
		where += " AND " + clause
	}
	if from := parseTime(params.Get("from")); from != nil {
		add("r.ts >= $"+strconv.Itoa(len(args)+1), *from)
	}
	if to := parseTime(params.Get("to")); to != nil {
		add("r.ts < $"+strconv.Itoa(len(args)+1), *to)
	}
	if model := params.Get("model"); model != "" {
		add("r.\"modelServed\" ILIKE $"+strconv.Itoa(len(args)+1), "%"+model+"%")
	}
	switch params.Get("status") {
	case "failed":
		where += " AND r.failed = true"
	case "success":
		where += " AND r.failed = false"
	}

	var total int64
	if err := s.pool.QueryRow(r.Context(),
		`SELECT count(*)::bigint FROM "UsageRecord" r `+where, args...).Scan(&total); err != nil {
		writeError(w, http.StatusServiceUnavailable, "usage_store_unavailable")
		return
	}

	args = append(args, pageSize, (page-1)*pageSize)
	sql := `SELECT r.rid, (extract(epoch from r.ts))::bigint, k."prefix4", k.fp6,
	               r.provider, r."modelReq", r."modelServed",
	               r."inputTokens", r."outputTokens", r."cacheReadInputTokens", r."cacheCreationInputTokens",
	               r."costCents", r.status, r.stream, r."latencyMs", r.unpriced, r."priceVersion"
	          FROM "UsageRecord" r JOIN "ApiKey" k ON k.id = r."keyId" ` + where +
		` ORDER BY r.seq DESC LIMIT $` + strconv.Itoa(len(args)-1) + ` OFFSET $` + strconv.Itoa(len(args))
	rows, err := s.pool.Query(r.Context(), sql, args...)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "usage_store_unavailable")
		return
	}
	defer rows.Close()

	out := make([]map[string]any, 0)
	for rows.Next() {
		var (
			rid, prefix4, fp6, provider, modelReq, modelServed, status, priceVersion string
			ts, in, outTokens, cacheRead, cacheWrite, cost                           int64
			stream, unpriced                                                         bool
			latency                                                                  *int32
		)
		if err := rows.Scan(&rid, &ts, &prefix4, &fp6, &provider, &modelReq, &modelServed,
			&in, &outTokens, &cacheRead, &cacheWrite, &cost, &status, &stream, &latency, &unpriced, &priceVersion); err != nil {
			writeError(w, http.StatusServiceUnavailable, "usage_store_unavailable")
			return
		}
		out = append(out, map[string]any{
			"rid": rid, "ts": time.Unix(ts, 0).UTC().Format(time.RFC3339),
			"key": prefix4 + "…" + fp6, "provider": provider,
			"modelReq": modelReq, "modelServed": modelServed,
			"inputTokens": in, "outputTokens": outTokens,
			"cacheReadInputTokens": cacheRead, "cacheCreationInputTokens": cacheWrite,
			"costCents": cost, "status": status, "stream": stream,
			"latencyMs": latency, "unpriced": unpriced, "priceVersion": priceVersion,
		})
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusServiceUnavailable, "usage_store_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": total, "page": page, "pageSize": pageSize, "rows": out})
}

func (s *Service) handleUsageSummary(w http.ResponseWriter, r *http.Request, info *virtualkey.KeyInfo) {
	params := r.URL.Query()
	rangeName := params.Get("range")
	from, to, groupBy := summaryWindow(rangeName, s.now())
	if custom := parseTime(params.Get("from")); custom != nil {
		from = *custom
	}
	if custom := parseTime(params.Get("to")); custom != nil {
		to = *custom
	}

	var totals struct {
		calls                                      int64
		errors                                     int64
		input, output, cacheRead, cacheWrite, cost int64
	}
	if err := s.pool.QueryRow(r.Context(),
		`SELECT count(*)::bigint,
		        COALESCE(sum(CASE WHEN failed THEN 1 ELSE 0 END), 0)::bigint,
		        COALESCE(sum("inputTokens"),0)::bigint,
		        COALESCE(sum("outputTokens"),0)::bigint,
		        COALESCE(sum("cacheReadInputTokens"),0)::bigint,
		        COALESCE(sum("cacheCreationInputTokens"),0)::bigint,
		        COALESCE(sum("costCents"),0)::bigint
		   FROM "UsageRecord"
		  WHERE "empId" = $1 AND ts >= $2 AND ts < $3`,
		info.EmpID, from, to).Scan(&totals.calls, &totals.errors, &totals.input, &totals.output,
		&totals.cacheRead, &totals.cacheWrite, &totals.cost); err != nil {
		writeError(w, http.StatusServiceUnavailable, "usage_store_unavailable")
		return
	}

	rows, err := s.pool.Query(r.Context(),
		`SELECT to_char(date_trunc($4, ts), 'YYYY-MM-DD') AS bucket,
		        count(*)::bigint,
		        COALESCE(sum(CASE WHEN failed THEN 1 ELSE 0 END),0)::bigint,
		        COALESCE(sum("inputTokens"),0)::bigint,
		        COALESCE(sum("outputTokens"),0)::bigint,
		        COALESCE(sum("costCents"),0)::bigint
		   FROM "UsageRecord"
		  WHERE "empId" = $1 AND ts >= $2 AND ts < $3
		  GROUP BY 1 ORDER BY 1`, info.EmpID, from, to, groupBy)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "usage_store_unavailable")
		return
	}
	defer rows.Close()

	buckets := make([]map[string]any, 0)
	for rows.Next() {
		var (
			bucket                             string
			calls, errors, input, output, cost int64
		)
		if err := rows.Scan(&bucket, &calls, &errors, &input, &output, &cost); err != nil {
			writeError(w, http.StatusServiceUnavailable, "usage_store_unavailable")
			return
		}
		buckets = append(buckets, map[string]any{
			"bucket": bucket, "calls": calls, "errors": errors,
			"inputTokens": input, "outputTokens": output, "costCents": cost,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from": from.UTC().Format(time.RFC3339), "to": to.UTC().Format(time.RFC3339),
		"group_by": groupBy,
		"totals": map[string]any{
			"calls": totals.calls, "errors": totals.errors,
			"inputTokens": totals.input, "outputTokens": totals.output,
			"cacheReadTokens": totals.cacheRead, "cacheWriteTokens": totals.cacheWrite,
			"costCents": totals.cost,
		},
		"buckets": buckets,
	})
}

// summaryWindow 计算统计窗口与分桶粒度；默认今天，week=近 7 天，month=本月。
func summaryWindow(rangeName string, now time.Time) (from, to time.Time, groupBy string) {
	to = now
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch rangeName {
	case "week":
		return startOfDay.AddDate(0, 0, -6), to, "day"
	case "month":
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()), to, "day"
	default:
		return startOfDay, to, "day"
	}
}

// ---------------------------------------------------------------- helpers

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func parseTime(value string) *time.Time {
	if value == "" {
		return nil
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return &parsed
	}
	if parsed, err := time.Parse("2006-01-02", value); err == nil {
		return &parsed
	}
	return nil
}

func atoiDefault(value string, fallback int) int {
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code}})
}

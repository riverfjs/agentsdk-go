package toolbuiltin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	xhtml "golang.org/x/net/html"

	"github.com/riverfjs/agentsdk-go/pkg/tool"
)

func init() {
	const shortToolDesc = "Search the web for up-to-date information."
	registerShortToolDesc("web_search", shortToolDesc)
}

const (
	webSearchDescription = `
	- Allows Agent to search the web and use the results to inform responses- Provides up-to-date information for current events and recent data
	- Returns search result information formatted as search result blocks
	- Use this tool for accessing information beyond Agent's knowledge cutoff
	- Searches are performed automatically within a single API call

	Usage notes:
	  - Domain filtering is supported to include or block specific websites
	  - Web search is only available in the US
	  - Account for \"Today's date\" in <env>. For example, if <env> says \"Today's date: 2025-07-01\", and the user wants the latest docs, do not use 2024 in the search query. Use 2025.
	`

	defaultSearchTimeout          = 12 * time.Second
	maxSearchTimeout              = 45 * time.Second
	defaultSearchMaxResults       = 8
	defaultSearchCacheTTL         = 90 * time.Second
	defaultSearchFailureTTL       = 45 * time.Second
	maxSearchResponseBytes    int = 1 << 20
	defaultSearchUserAgent        = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"
	duckDuckGoFormContentType     = "application/x-www-form-urlencoded"
)

const duckDuckGoDefaultEndpoint = "https://html.duckduckgo.com/html/"

var duckDuckGoEndpoint = duckDuckGoDefaultEndpoint

var webSearchSchema = &tool.JSONSchema{
	Type: "object",
	Properties: map[string]interface{}{
		"query": map[string]interface{}{
			"type":        "string",
			"minLength":   2,
			"description": "The search query to use",
		},
		"allowed_domains": map[string]interface{}{
			"type": "array",
			"items": map[string]interface{}{
				"type": "string",
			},
			"description": "Only include search results from these domains",
		},
		"blocked_domains": map[string]interface{}{
			"type": "array",
			"items": map[string]interface{}{
				"type": "string",
			},
			"description": "Never include search results from these domains",
		},
	},
	Required: []string{"query"},
}

// WebSearchOptions configures HTTP behaviour for WebSearchTool.
type WebSearchOptions struct {
	HTTPClient *http.Client
	Timeout    time.Duration
	MaxResults int
	CacheTTL   time.Duration
	FailureTTL time.Duration
}

// WebSearchTool proxies search queries to an HTTP endpoint and filters domains.
type WebSearchTool struct {
	client     *http.Client
	timeout    time.Duration
	maxResults int
	cacheTTL   time.Duration
	failureTTL time.Duration
	now        func() time.Time
	mu         sync.RWMutex
	cache      map[string]searchCacheEntry
	failures   map[string]searchFailureEntry
}

type searchCacheEntry struct {
	expiresAt time.Time
	results   []SearchResult
}

type searchFailureEntry struct {
	expiresAt time.Time
	status    int
}

// NewWebSearchTool constructs a search tool with defaults.
func NewWebSearchTool(opts *WebSearchOptions) *WebSearchTool {
	cfg := WebSearchOptions{}
	if opts != nil {
		cfg = *opts
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultSearchTimeout
	} else if timeout > maxSearchTimeout {
		timeout = maxSearchTimeout
	}
	maxResults := cfg.MaxResults
	if maxResults <= 0 {
		maxResults = defaultSearchMaxResults
	}
	cacheTTL := cfg.CacheTTL
	if cacheTTL <= 0 {
		cacheTTL = defaultSearchCacheTTL
	}
	failureTTL := cfg.FailureTTL
	if failureTTL <= 0 {
		failureTTL = defaultSearchFailureTTL
	}
	client := cloneHTTPClient(cfg.HTTPClient)
	client.Timeout = timeout

	return &WebSearchTool{
		client:     client,
		timeout:    timeout,
		maxResults: maxResults,
		cacheTTL:   cacheTTL,
		failureTTL: failureTTL,
		now:        time.Now,
		cache:      make(map[string]searchCacheEntry),
		failures:   make(map[string]searchFailureEntry),
	}
}

func (w *WebSearchTool) Name() string { return "WebSearch" }

func (w *WebSearchTool) Description() string { return webSearchDescription }

func (w *WebSearchTool) Schema() *tool.JSONSchema { return webSearchSchema }

// Execute performs a remote search and filters results using domain lists.
func (w *WebSearchTool) Execute(ctx context.Context, params map[string]interface{}) (*tool.ToolResult, error) {
	if ctx == nil {
		return nil, errors.New("context is nil")
	}
	if w == nil || w.client == nil {
		return nil, errors.New("web search tool is not initialised")
	}
	if params == nil {
		return nil, errors.New("params is nil")
	}

	query, err := extractNonEmptyString(params, "query")
	if err != nil {
		return nil, err
	}
	if len([]rune(query)) < 2 {
		return nil, errors.New("query must contain at least 2 characters")
	}

	allowed, err := parseDomainList(params, "allowed_domains")
	if err != nil {
		return nil, err
	}
	blocked, err := parseDomainList(params, "blocked_domains")
	if err != nil {
		return nil, err
	}
	cacheKey := buildSearchCacheKey(query, allowed, blocked)
	now := w.now()
	if status, blockedRepeat := w.getSuppressedFailure(cacheKey, now); blockedRepeat {
		return nil, fmt.Errorf("search skipped: previous request failed with status %d (cooldown active)", status)
	}
	if cached, ok := w.getCachedResults(cacheKey, now); ok {
		filtered := filterResultsByDomain(cached, allowed, blocked, w.maxResults)
		return &tool.ToolResult{
			Success: true,
			Output:  formatSearchOutput(query, filtered),
			Data: map[string]interface{}{
				"query":           query,
				"results":         filtered,
				"allowed_domains": allowed,
				"blocked_domains": blocked,
				"cache_hit":       true,
			},
		}, nil
	}

	reqCtx := ctx
	var cancel context.CancelFunc
	if w.timeout > 0 {
		reqCtx, cancel = context.WithTimeout(ctx, w.timeout)
		defer cancel()
	}

	results, err := w.search(reqCtx, query)
	if err != nil {
		if status, ok := statusFromError(err); ok && shouldSuppressStatus(status) {
			w.setSuppressedFailure(cacheKey, status, now)
		}
		return nil, err
	}
	w.setCachedResults(cacheKey, results, now)
	w.clearSuppressedFailure(cacheKey)

	filtered := filterResultsByDomain(results, allowed, blocked, w.maxResults)

	output := formatSearchOutput(query, filtered)
	data := map[string]interface{}{
		"query":           query,
		"results":         filtered,
		"allowed_domains": allowed,
		"blocked_domains": blocked,
		"cache_hit":       false,
	}

	return &tool.ToolResult{
		Success: true,
		Output:  output,
		Data:    data,
	}, nil
}

type searchStatusError struct {
	status int
}

func (e *searchStatusError) Error() string {
	if e == nil {
		return "search failed"
	}
	return fmt.Sprintf("search failed with status %d", e.status)
}

func (w *WebSearchTool) search(ctx context.Context, query string) ([]SearchResult, error) {
	endpoint := strings.TrimSpace(duckDuckGoEndpoint)
	if endpoint == "" {
		return nil, errors.New("duckduckgo endpoint is not configured")
	}
	form := url.Values{}
	form.Set("q", query)
	form.Set("kl", "us-en")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", defaultSearchUserAgent)
	req.Header.Set("Content-Type", duckDuckGoFormContentType)

	resp, err := w.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, &searchStatusError{status: resp.StatusCode}
	}

	reader := io.LimitReader(resp.Body, int64(maxSearchResponseBytes)+1)
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read search response: %w", err)
	}
	if len(body) > maxSearchResponseBytes {
		return nil, fmt.Errorf("search response exceeded %d bytes", maxSearchResponseBytes)
	}

	// DDG returns 202 with a CAPTCHA/anomaly challenge when bot traffic is detected.
	if resp.StatusCode == 202 || bytes.Contains(body, []byte("challenge-form")) {
		return nil, fmt.Errorf("DuckDuckGo CAPTCHA triggered — use the browser skill to navigate to https://duckduckgo.com, take a screenshot to show the user the CAPTCHA page, then use the browser to perform the search directly and extract results from the page")
	}

	doc, err := xhtml.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse search HTML: %w", err)
	}
	return extractDuckDuckGoResults(doc), nil
}

// SearchResult describes a single search hit.
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

func extractDuckDuckGoResults(doc *xhtml.Node) []SearchResult {
	if doc == nil {
		return nil
	}
	results := make([]SearchResult, 0, 8)
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.ElementNode && n.Data == "div" && nodeHasClass(n, "result") {
			if res := buildDuckDuckGoResult(n); res != nil {
				results = append(results, *res)
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return deduplicateResults(results)
}

func buildDuckDuckGoResult(node *xhtml.Node) *SearchResult {
	var title, urlStr, snippet, fallbackURL string
	var inspect func(*xhtml.Node)
	inspect = func(n *xhtml.Node) {
		if n.Type == xhtml.ElementNode {
			if urlStr == "" && n.Data == "a" && nodeHasClass(n, "result__a") {
				if href := getAttr(n, "href"); href != "" {
					urlStr = cleanResultURL(href)
				}
				if title == "" {
					title = nodeText(n)
				}
			}
			if snippet == "" && nodeHasClass(n, "result__snippet") {
				snippet = nodeText(n)
			}
			if fallbackURL == "" && nodeHasClass(n, "result__url") {
				fallbackURL = nodeText(n)
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			inspect(child)
		}
	}
	inspect(node)

	if urlStr == "" && fallbackURL != "" {
		urlStr = cleanResultURL(fallbackURL)
	}
	if urlStr == "" || title == "" {
		return nil
	}
	return &SearchResult{
		Title:   title,
		URL:     urlStr,
		Snippet: snippet,
	}
}

func nodeHasClass(n *xhtml.Node, class string) bool {
	if n == nil {
		return false
	}
	attr := getAttr(n, "class")
	if attr == "" {
		return false
	}
	for _, part := range strings.Fields(attr) {
		if part == class {
			return true
		}
	}
	return false
}

func getAttr(n *xhtml.Node, key string) string {
	if n == nil {
		return ""
	}
	for _, attr := range n.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}
	return ""
}

func nodeText(n *xhtml.Node) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	collectNodeText(n, &b)
	return collapseWhitespace(b.String())
}

func collectNodeText(n *xhtml.Node, b *strings.Builder) {
	if n == nil {
		return
	}
	switch n.Type {
	case xhtml.TextNode:
		b.WriteString(n.Data)
	case xhtml.ElementNode:
		if n.Data == "br" {
			b.WriteByte(' ')
		}
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		collectNodeText(child, b)
	}
}

func collapseWhitespace(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return strings.Join(strings.Fields(text), " ")
}

func cleanResultURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if decoded, err := url.QueryUnescape(raw); err == nil {
		raw = decoded
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	if parsed.Hostname() == "" {
		return ""
	}
	parsed.Fragment = ""
	return parsed.String()
}

func deduplicateResults(results []SearchResult) []SearchResult {
	if len(results) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(results))
	dedup := make([]SearchResult, 0, len(results))
	for _, res := range results {
		if res.URL == "" {
			continue
		}
		if _, ok := seen[res.URL]; ok {
			continue
		}
		seen[res.URL] = struct{}{}
		dedup = append(dedup, res)
	}
	if len(dedup) == 0 {
		return nil
	}
	return dedup
}

func parseDomainList(params map[string]interface{}, key string) ([]string, error) {
	raw, ok := params[key]
	if !ok || raw == nil {
		return nil, nil
	}
	switch v := raw.(type) {
	case []string:
		return normaliseDomains(v), nil
	case []interface{}:
		items := make([]string, 0, len(v))
		for _, item := range v {
			if item == nil {
				continue
			}
			str, err := stringValue(item)
			if err != nil {
				return nil, fmt.Errorf("%s contains non-string values: %w", key, err)
			}
			items = append(items, str)
		}
		return normaliseDomains(items), nil
	default:
		return nil, fmt.Errorf("%s must be an array of strings", key)
	}
}

func normaliseDomains(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, v := range values {
		trimmed := strings.ToLower(strings.TrimSpace(v))
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		result = append(result, trimmed)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func filterResultsByDomain(results []SearchResult, allowed, blocked []string, limit int) []SearchResult {
	filtered := make([]SearchResult, 0, len(results))
	for _, res := range results {
		if limit > 0 && len(filtered) >= limit {
			break
		}
		host := extractHost(res.URL)
		if host == "" {
			continue
		}
		if inDomainList(host, blocked) {
			continue
		}
		if len(allowed) > 0 && !inDomainList(host, allowed) {
			continue
		}
		filtered = append(filtered, res)
	}
	return filtered
}

func extractHost(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

func inDomainList(host string, domains []string) bool {
	if len(domains) == 0 {
		return false
	}
	for _, domain := range domains {
		if domainMatches(host, domain) {
			return true
		}
	}
	return false
}

func formatSearchOutput(query string, results []SearchResult) string {
	if len(results) == 0 {
		return fmt.Sprintf("No results for %q", query)
	}
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Search results for %q:\n", query))
	for i, res := range results {
		builder.WriteString(fmt.Sprintf("%d. %s\n", i+1, strings.TrimSpace(res.Title)))
		builder.WriteString(fmt.Sprintf("   %s\n", strings.TrimSpace(res.URL)))
		if strings.TrimSpace(res.Snippet) != "" {
			builder.WriteString(fmt.Sprintf("   %s\n", strings.TrimSpace(res.Snippet)))
		}
	}
	return strings.TrimSpace(builder.String())
}

func buildSearchCacheKey(query string, allowed, blocked []string) string {
	q := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(query)), " "))
	allow := append([]string(nil), allowed...)
	block := append([]string(nil), blocked...)
	sort.Strings(allow)
	sort.Strings(block)
	return q + "|a:" + strings.Join(allow, ",") + "|b:" + strings.Join(block, ",")
}

func (w *WebSearchTool) getCachedResults(key string, now time.Time) ([]SearchResult, bool) {
	w.mu.RLock()
	entry, ok := w.cache[key]
	w.mu.RUnlock()
	if !ok || !entry.expiresAt.After(now) {
		if ok {
			w.mu.Lock()
			delete(w.cache, key)
			w.mu.Unlock()
		}
		return nil, false
	}
	out := make([]SearchResult, len(entry.results))
	copy(out, entry.results)
	return out, true
}

func (w *WebSearchTool) setCachedResults(key string, results []SearchResult, now time.Time) {
	if len(results) == 0 {
		return
	}
	out := make([]SearchResult, len(results))
	copy(out, results)
	w.mu.Lock()
	w.cache[key] = searchCacheEntry{
		expiresAt: now.Add(w.cacheTTL),
		results:   out,
	}
	w.mu.Unlock()
}

func (w *WebSearchTool) getSuppressedFailure(key string, now time.Time) (int, bool) {
	w.mu.RLock()
	entry, ok := w.failures[key]
	w.mu.RUnlock()
	if !ok || !entry.expiresAt.After(now) {
		if ok {
			w.mu.Lock()
			delete(w.failures, key)
			w.mu.Unlock()
		}
		return 0, false
	}
	return entry.status, true
}

func (w *WebSearchTool) setSuppressedFailure(key string, status int, now time.Time) {
	w.mu.Lock()
	w.failures[key] = searchFailureEntry{
		expiresAt: now.Add(w.failureTTL),
		status:    status,
	}
	w.mu.Unlock()
}

func (w *WebSearchTool) clearSuppressedFailure(key string) {
	w.mu.Lock()
	delete(w.failures, key)
	w.mu.Unlock()
}

func statusFromError(err error) (int, bool) {
	var statusErr *searchStatusError
	if errors.As(err, &statusErr) && statusErr != nil {
		return statusErr.status, true
	}
	return 0, false
}

func shouldSuppressStatus(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusNotFound
}

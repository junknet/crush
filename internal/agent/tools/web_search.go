package tools

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"charm.land/fantasy"
)

type webSearchResult struct {
	Title   string
	URL     string
	Snippet string
}

//go:embed web_search.md.tpl
var webSearchDescriptionTmpl []byte

var webSearchDescriptionTpl = template.Must(
	template.New("webSearchDescription").
		Parse(string(webSearchDescriptionTmpl)),
)

// NewWebSearchTool creates a web search tool for sub-agents (no permissions needed).
func NewWebSearchTool(client *http.Client) fantasy.AgentTool {
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.MaxIdleConns = 100
		transport.MaxIdleConnsPerHost = 10
		transport.IdleConnTimeout = 90 * time.Second

		client = &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		}
	}

	return fantasy.NewParallelAgentTool(
		WebSearchToolName,
		renderToolDescription(webSearchDescriptionTpl),
		func(ctx context.Context, params WebSearchParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Query == "" {
				return fantasy.NewTextErrorResponse("query is required"), nil
			}

			maxResults := params.MaxResults
			if maxResults <= 0 {
				maxResults = 10
			}
			if maxResults > 20 {
				maxResults = 20
			}

			maybeDelaySearch()
			results, err := searchDuckDuckGo(ctx, client, params.Query, maxResults)
			slog.Debug("Web search completed", "query", params.Query, "results", len(results), "err", err)
			if err != nil {
				return fantasy.NewTextErrorResponse("Failed to search: " + err.Error()), nil
			}

			return fantasy.NewTextResponse(formatSearchResults(results)), nil
		},
	)
}

func maybeDelaySearch() {}

func searchDuckDuckGo(ctx context.Context, client *http.Client, query string, maxResults int) ([]webSearchResult, error) {
	endpoint := "https://api.duckduckgo.com/"
	reqURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	q := reqURL.Query()
	q.Set("q", query)
	q.Set("format", "json")
	q.Set("no_html", "1")
	q.Set("skip_disambig", "1")
	reqURL.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "crush-web-search")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("DuckDuckGo returned HTTP %d", resp.StatusCode)
	}

	var payload struct {
		AbstractText  string `json:"AbstractText"`
		AbstractURL   string `json:"AbstractURL"`
		Heading       string `json:"Heading"`
		RelatedTopics []struct {
			FirstURL string `json:"FirstURL"`
			Text     string `json:"Text"`
			Topics   []struct {
				FirstURL string `json:"FirstURL"`
				Text     string `json:"Text"`
			} `json:"Topics"`
		} `json:"RelatedTopics"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}

	results := make([]webSearchResult, 0, maxResults)
	if payload.AbstractText != "" || payload.AbstractURL != "" {
		results = append(results, webSearchResult{
			Title:   firstNonEmptyString(payload.Heading, query),
			URL:     payload.AbstractURL,
			Snippet: payload.AbstractText,
		})
	}
	for _, topic := range payload.RelatedTopics {
		if len(results) >= maxResults {
			break
		}
		if topic.Text != "" || topic.FirstURL != "" {
			results = append(results, webSearchResult{
				Title:   searchResultTitle(topic.Text),
				URL:     topic.FirstURL,
				Snippet: topic.Text,
			})
		}
		for _, nested := range topic.Topics {
			if len(results) >= maxResults {
				break
			}
			results = append(results, webSearchResult{
				Title:   searchResultTitle(nested.Text),
				URL:     nested.FirstURL,
				Snippet: nested.Text,
			})
		}
	}
	return results, nil
}

func formatSearchResults(results []webSearchResult) string {
	if len(results) == 0 {
		return "No search results found"
	}
	var out strings.Builder
	for i, result := range results {
		fmt.Fprintf(&out, "%d. %s\n", i+1, result.Title)
		if result.URL != "" {
			fmt.Fprintf(&out, "   URL: %s\n", result.URL)
		}
		if result.Snippet != "" {
			fmt.Fprintf(&out, "   %s\n", result.Snippet)
		}
	}
	return strings.TrimSpace(out.String())
}

func searchResultTitle(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "Untitled"
	}
	if idx := strings.Index(text, " - "); idx > 0 {
		return text[:idx]
	}
	if len(text) > 80 {
		return text[:80] + "..."
	}
	return text
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

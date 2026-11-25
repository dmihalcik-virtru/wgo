package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// BulkIssueResult is one requested work item's result from Jira bulkfetch.
type BulkIssueResult struct {
	Input string
	Data  json.RawMessage
	Err   error
}

// BulkGetIssuesJSONContext fetches multiple work items in one twg call. An
// individual missing or inaccessible key is reported on that result while
// other results remain usable.
func BulkGetIssuesJSONContext(ctx context.Context, tickets []string, fields string) ([]BulkIssueResult, error) {
	if len(tickets) == 0 {
		return nil, nil
	}
	args := append([]string{"jira", "workitem", "bulk-get"}, tickets...)
	if fields == "*all" {
		args = append(args, "--full")
	} else if fields != "" {
		args = append(args, "--fields", fields)
	}
	data, err := runContext(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("bulk get issues: %w", err)
	}
	var page struct {
		Items []struct {
			Input string          `json:"input"`
			OK    bool            `json:"ok"`
			Data  json.RawMessage `json:"data"`
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &page); err != nil {
		return nil, fmt.Errorf("parse bulk issues: %w", err)
	}
	byInput := make(map[string]BulkIssueResult, len(page.Items))
	for _, item := range page.Items {
		result := BulkIssueResult{Input: item.Input, Data: item.Data}
		if !item.OK || len(item.Data) == 0 || string(item.Data) == "null" {
			msg := strings.TrimSpace(item.Error.Message)
			if msg == "" {
				msg = "work item was not returned"
			}
			if item.Error.Code != "" {
				msg = item.Error.Code + ": " + msg
			}
			result.Data = nil
			if IsAuthError(msg) {
				result.Err = fmt.Errorf("get issue %s: %w: %s", item.Input, ErrNotLoggedIn, msg)
			} else {
				result.Err = fmt.Errorf("get issue %s: %s", item.Input, msg)
			}
		}
		byInput[item.Input] = result
	}
	results := make([]BulkIssueResult, 0, len(tickets))
	for _, ticket := range tickets {
		if result, ok := byInput[ticket]; ok {
			results = append(results, result)
		} else {
			results = append(results, BulkIssueResult{Input: ticket, Err: fmt.Errorf("get issue %s: work item was not returned", ticket)})
		}
	}
	return results, nil
}

package domain

import (
	"strings"

	"github.com/vance1852/drviercar/internal/apperr"
)

// DefaultPageSize and MaxPageSize bound list endpoints.
const (
	DefaultPageSize = 20
	MaxPageSize     = 100
)

// SortDirection is the ordering direction of a list query.
type SortDirection string

const (
	SortAsc  SortDirection = "asc"
	SortDesc SortDirection = "desc"
)

// PageRequest describes pagination, filtering and ordering of a list query.
type PageRequest struct {
	Page      int
	PageSize  int
	SortField string
	SortDir   SortDirection
}

// Normalize applies defaults and validates the sort field against a whitelist.
func (p PageRequest) Normalize(allowedSort map[string]string, defaultSort string) (PageRequest, error) {
	normalized := p
	if normalized.Page <= 0 {
		normalized.Page = 1
	}
	if normalized.PageSize <= 0 {
		normalized.PageSize = DefaultPageSize
	}
	if normalized.PageSize > MaxPageSize {
		return PageRequest{}, apperr.Invalidf("page_size_too_large",
			"单页最多返回 %d 条记录", MaxPageSize)
	}
	field := strings.TrimSpace(normalized.SortField)
	if field == "" {
		field = defaultSort
	}
	if _, ok := allowedSort[field]; !ok {
		return PageRequest{}, apperr.Invalidf("sort_field_not_allowed", "不支持按 %q 排序", field)
	}
	normalized.SortField = field
	if normalized.SortDir != SortAsc && normalized.SortDir != SortDesc {
		normalized.SortDir = SortDesc
	}
	return normalized, nil
}

// Offset reports the SQL offset for the page.
func (p PageRequest) Offset() int {
	return (p.Page - 1) * p.PageSize
}

// OrderClause renders the validated ORDER BY fragment.
func (p PageRequest) OrderClause(allowedSort map[string]string) string {
	column := allowedSort[p.SortField]
	direction := "DESC"
	if p.SortDir == SortAsc {
		direction = "ASC"
	}
	return column + " " + direction
}

// PageMeta describes the pagination envelope of a list response.
type PageMeta struct {
	Page       int  `json:"page"`
	PageSize   int  `json:"page_size"`
	Total      int  `json:"total"`
	TotalPages int  `json:"total_pages"`
	HasNext    bool `json:"has_next"`
}

// NewPageMeta builds the pagination envelope for a query result.
func NewPageMeta(request PageRequest, total int) PageMeta {
	totalPages := 0
	if request.PageSize > 0 {
		totalPages = (total + request.PageSize - 1) / request.PageSize
	}
	return PageMeta{
		Page:       request.Page,
		PageSize:   request.PageSize,
		Total:      total,
		TotalPages: totalPages,
		HasNext:    request.Page < totalPages,
	}
}

// BatchItemResult is the per-item outcome of a batch operation.
type BatchItemResult struct {
	Reference string `json:"reference"`
	Applied   bool   `json:"applied"`
	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
}

// BatchOutcome aggregates a partial-failure batch operation.
type BatchOutcome struct {
	Requested int               `json:"requested"`
	Applied   int               `json:"applied"`
	Failed    int               `json:"failed"`
	Items     []BatchItemResult `json:"items"`
}

// Add appends one item outcome and keeps the counters consistent.
func (o *BatchOutcome) Add(result BatchItemResult) {
	o.Requested++
	if result.Applied {
		o.Applied++
	} else {
		o.Failed++
	}
	o.Items = append(o.Items, result)
}

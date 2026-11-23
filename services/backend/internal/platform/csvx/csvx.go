// Package csvx holds the shared plumbing for all-or-nothing CSV imports: reading the upload,
// mapping header aliases to fields, typed cell parsing and the IMPORT_REJECTED error.
package csvx

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/httpx"
)

const (
	MaxRows         = 10_000
	MaxBytes        = 5 << 20
	maxReportErrors = 100
)

// RowError points at one bad cell. Row is the 1-based line number in the file (header = 1).
type RowError struct {
	Row     int    `json:"row"`
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Body accepts a raw text/csv body or a multipart form with a "file" field (max 5 MB).
func Body(w http.ResponseWriter, r *http.Request) (io.Reader, error) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxBytes)
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch mediaType {
	case "text/csv", "application/csv", "text/plain":
		return r.Body, nil
	case "multipart/form-data":
		f, _, err := r.FormFile("file")
		if err != nil {
			return nil, httpx.BadRequest("INVALID_UPLOAD", "multipart upload needs a 'file' field (max 5 MB)", nil)
		}
		return f, nil
	}
	return nil, &httpx.Error{Status: http.StatusUnsupportedMediaType, Code: "UNSUPPORTED_MEDIA_TYPE",
		Message: "send text/csv or multipart/form-data with a 'file' field"}
}

// Row is one data line. Cells are keyed by canonical field name; Errs collects parse problems.
type Row struct {
	Line  int
	cells map[string]string
	Errs  []RowError
}

// Get returns the trimmed cell for field, or "" when the column is absent or empty.
func (r *Row) Get(field string) string { return r.cells[field] }

// String returns a pointer to the cell, or nil when it is empty.
func (r *Row) String(field string) *string {
	if v := r.cells[field]; v != "" {
		return &v
	}
	return nil
}

// Float parses the cell as a number; empty gives nil, garbage records a row error.
func (r *Row) Float(field string) *float64 {
	v := r.cells[field]
	if v == "" {
		return nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		r.Errs = append(r.Errs, RowError{r.Line, field, "not a number"})
		return nil
	}
	return &f
}

// Int32 parses the cell as an integer; empty gives nil, garbage records a row error.
func (r *Row) Int32(field string) *int32 {
	v := r.cells[field]
	if v == "" {
		return nil
	}
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil {
		r.Errs = append(r.Errs, RowError{r.Line, field, "not an integer"})
		return nil
	}
	n32 := int32(n)
	return &n32
}

// Problems turns a field -> message map from a validator into row errors. A field that already
// has an error (e.g. "not a number", which leaves it unset) is not reported again as "required".
func (r *Row) Problems(p map[string]string) {
	seen := map[string]bool{}
	for _, e := range r.Errs {
		seen[e.Field] = true
	}
	for field, msg := range p {
		if !seen[field] {
			r.Errs = append(r.Errs, RowError{r.Line, field, msg})
		}
	}
}

// Parse reads the header, maps columns through aliases (case-insensitive; field -> accepted
// names) and returns the data rows. Header problems come back as one INVALID_CSV error.
func Parse(body io.Reader, aliases map[string][]string, required []string) ([]*Row, error) {
	rows, err := parse(body, aliases, required)
	if err != nil {
		return nil, httpx.BadRequest("INVALID_CSV", err.Error(), nil)
	}
	return rows, nil
}

func parse(body io.Reader, aliases map[string][]string, required []string) ([]*Row, error) {
	cr := csv.NewReader(body)
	cr.TrimLeadingSpace = true
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if errors.Is(err, io.EOF) {
		return nil, errors.New("the file is empty")
	} else if err != nil {
		return nil, fmt.Errorf("unreadable CSV header: %w", err)
	}

	col := map[string]int{}
	for i, h := range header {
		h = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff"))) // Excel BOM
		for field, names := range aliases {
			for _, a := range names {
				if h != a {
					continue
				}
				if _, dup := col[field]; dup {
					return nil, fmt.Errorf("column for %q appears twice", field)
				}
				col[field] = i
			}
		}
	}
	var missing []string
	for _, f := range required {
		if _, ok := col[f]; !ok {
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required columns: %s", strings.Join(missing, ", "))
	}

	var rows []*Row
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		line, _ := cr.FieldPos(0)
		if err != nil {
			rows = append(rows, &Row{Line: line, Errs: []RowError{{line, "", err.Error()}}})
			continue
		}
		if len(rows) >= MaxRows {
			return nil, fmt.Errorf("more than %d rows; split the file", MaxRows)
		}
		row := &Row{Line: line, cells: map[string]string{}}
		for field, i := range col {
			if i < len(rec) {
				row.cells[field] = strings.TrimSpace(rec[i])
			}
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil, errors.New("the file has a header but no rows")
	}
	return rows, nil
}

// Errors gathers every row's errors, sorted by line.
func Errors(rows []*Row) []RowError {
	var errs []RowError
	for _, r := range rows {
		errs = append(errs, r.Errs...)
	}
	sort.SliceStable(errs, func(i, j int) bool {
		if errs[i].Row != errs[j].Row {
			return errs[i].Row < errs[j].Row
		}
		return errs[i].Field < errs[j].Field
	})
	return errs
}

// Rejected is the 422 returned when any row is invalid; nothing is written. It reports at most
// 100 errors plus the total, in the same shape as the orders import.
func Rejected(errs []RowError, rows int) error {
	total := len(errs)
	if total > maxReportErrors {
		errs = errs[:maxReportErrors]
	}
	return httpx.Unprocessable("IMPORT_REJECTED",
		fmt.Sprintf("%d problem(s) found; nothing was imported", total),
		map[string]any{"errors": errs, "total_errors": total, "rows": rows})
}

// Key normalizes a natural key (name, registration) for duplicate detection.
func Key(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

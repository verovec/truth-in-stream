package parliament

import (
	"archive/zip"
	"bufio"
	"fmt"
	"io"
	"strings"
)

// maxCopyLineBytes bounds one COPY data line the scanner buffers. A Senat scr row
// carries a long scrutin objet, so the bound is generous while still rejecting a
// pathological line.
const maxCopyLineBytes = 8 << 20 // 8 MiB

// copyRowFunc receives one parsed COPY data row: the table name, its declared
// columns (in dump order), and the row's field values (NULL rendered as ""). A
// non-nil return aborts the stream.
type copyRowFunc func(table string, cols, fields []string) error

// streamCopyFromDumpZip opens the single .sql entry inside a Senat pg_dump zip and
// streams every COPY data row of a table in want to handle, without loading the
// (very large) dump into memory. Tables not in want are skipped, including their
// data rows. It makes no promise about the order the tables appear in: callers that
// join across tables buffer the dependent rows and resolve them after the whole
// stream completes, so the dump's COPY order does not matter.
func streamCopyFromDumpZip(archivePath string, want map[string]bool, handle copyRowFunc) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("parliament: open dump: %w", err)
	}
	defer func() { _ = zr.Close() }()
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || fileExt(f.Name) != ".sql" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("parliament: open dump entry %q: %w", f.Name, err)
		}
		err = streamCopy(rc, want, handle)
		_ = rc.Close()
		return err
	}
	return fmt.Errorf("parliament: no .sql entry in dump")
}

// streamCopy scans a PostgreSQL dump, invoking handle for each data row of a table
// in want. It tracks the current COPY block: a "COPY <table> (<cols>) FROM stdin;"
// line for a wanted table opens a block whose rows (until the terminating "\.") are
// handled; a block for an unwanted table is skipped.
func streamCopy(r io.Reader, want map[string]bool, handle copyRowFunc) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), maxCopyLineBytes)

	var table string
	var cols []string
	for sc.Scan() {
		line := sc.Text()
		if table == "" {
			if name, c, ok := parseCopyHeader(line); ok && want[name] {
				table, cols = name, c
			}
			continue
		}
		if line == `\.` {
			table, cols = "", nil
			continue
		}
		if err := handle(table, cols, splitCopyRow(line)); err != nil {
			return err
		}
	}
	return sc.Err()
}

// parseCopyHeader parses a "COPY <table> (<c1, c2, ...>) FROM stdin;" line into the
// table name and its columns, reporting whether the line is such a header.
func parseCopyHeader(line string) (table string, cols []string, ok bool) {
	if !strings.HasPrefix(line, "COPY ") {
		return "", nil, false
	}
	open := strings.IndexByte(line, '(')
	closeIdx := strings.IndexByte(line, ')')
	if open < 0 || closeIdx < open {
		return "", nil, false
	}
	table = strings.TrimSpace(line[len("COPY "):open])
	for _, c := range strings.Split(line[open+1:closeIdx], ",") {
		cols = append(cols, strings.TrimSpace(c))
	}
	return table, cols, true
}

// splitCopyRow splits one COPY data line into its fields. In the text format a real
// tab in a value is escaped as "\t", so a raw tab is always a field separator; each
// field is then unescaped and a lone "\N" becomes an empty string (NULL).
func splitCopyRow(line string) []string {
	raw := strings.Split(line, "\t")
	out := make([]string, len(raw))
	for i, f := range raw {
		if f == `\N` {
			out[i] = ""
			continue
		}
		out[i] = unescapeCopyField(f)
	}
	return out
}

// unescapeCopyField reverses the COPY text-format escapes (\t \n \r \\) in one
// field. Other backslash sequences are left as-is, which is harmless for the plain
// textual fields this connector reads.
func unescapeCopyField(f string) string {
	if !strings.ContainsRune(f, '\\') {
		return f
	}
	var b strings.Builder
	b.Grow(len(f))
	for i := 0; i < len(f); i++ {
		if f[i] == '\\' && i+1 < len(f) {
			switch f[i+1] {
			case 't':
				b.WriteByte('\t')
				i++
				continue
			case 'n':
				b.WriteByte('\n')
				i++
				continue
			case 'r':
				b.WriteByte('\r')
				i++
				continue
			case '\\':
				b.WriteByte('\\')
				i++
				continue
			}
		}
		b.WriteByte(f[i])
	}
	return b.String()
}

// colIndex maps a table's columns to their positions, so a handler reads fields by
// name rather than a brittle position.
func colIndex(cols []string) map[string]int {
	idx := make(map[string]int, len(cols))
	for i, c := range cols {
		idx[c] = i
	}
	return idx
}

// field returns the named column of a row, or "" when absent or out of range.
func field(idx map[string]int, fields []string, name string) string {
	i, ok := idx[name]
	if !ok || i >= len(fields) {
		return ""
	}
	return strings.TrimSpace(fields[i])
}

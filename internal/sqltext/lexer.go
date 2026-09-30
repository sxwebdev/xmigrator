// Package sqltext scans SQL without altering statement boundaries or quoted bodies.
package sqltext

import (
	"fmt"
	"strings"
)

type Token struct {
	Text            string
	Start, End      int
	Comment, Quoted bool
}

func Scan(s string) ([]Token, error) { return ScanDialect(s, "pgx") }

func ScanDialect(s, dialect string) ([]Token, error) {
	var out []Token
	for i := 0; i < len(s); {
		if strings.ContainsRune(" \t\r\n\f\v", rune(s[i])) {
			i++
			continue
		}
		start := i
		switch {
		case strings.HasPrefix(s[i:], "--"):
			i += 2
			for i < len(s) && s[i] != '\n' && (dialect == "sqlite" || s[i] != '\r') {
				i++
			}
			out = append(out, Token{s[start:i], start, i, true, false})
		case strings.HasPrefix(s[i:], "/*"):
			i += 2
			depth := 1
			for i < len(s) && depth > 0 {
				if dialect != "sqlite" && strings.HasPrefix(s[i:], "/*") {
					depth++
					i += 2
				} else if strings.HasPrefix(s[i:], "*/") {
					depth--
					i += 2
				} else {
					i++
				}
			}
			if depth != 0 {
				return nil, fmt.Errorf("unterminated comment")
			}
			out = append(out, Token{s[start:i], start, i, true, false})
		case s[i] == '\'' || s[i] == '"' || dialect == "sqlite" && (s[i] == '`' || s[i] == '['):
			quote := s[i]
			end := quote
			if quote == '[' {
				end = ']'
			}
			i++
			closed := false
			escape := dialect != "sqlite" && quote == '\'' && start > 0 && (s[start-1] == 'E' || s[start-1] == 'e') && (start == 1 || !word(s[start-2]))
			for i < len(s) {
				if escape && s[i] == '\\' {
					i += 2
					continue
				}
				if s[i] == end {
					i++
					if (dialect != "sqlite" || quote != '[') && i < len(s) && s[i] == end {
						i++
						continue
					}
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, fmt.Errorf("unterminated quote")
			}
			out = append(out, Token{s[start:i], start, i, false, true})
		case s[i] == '$' && dialect != "sqlite":
			j := i + 1
			for j < len(s) && ((s[j] >= 'a' && s[j] <= 'z') || (s[j] >= 'A' && s[j] <= 'Z') || s[j] == '_' || (j > i+1 && s[j] >= '0' && s[j] <= '9')) {
				j++
			}
			if j < len(s) && s[j] == '$' {
				tag := s[i : j+1]
				k := strings.Index(s[j+1:], tag)
				if k < 0 {
					return nil, fmt.Errorf("unterminated dollar quote")
				}
				i = j + 1 + k + len(tag)
				out = append(out, Token{s[start:i], start, i, false, true})
			} else {
				i++
				out = append(out, Token{s[start:i], start, i, false, false})
			}
		default:
			i++
			if word(s[start]) {
				for i < len(s) && (word(s[i]) || s[i] == '$') {
					i++
				}
			}
			out = append(out, Token{s[start:i], start, i, false, false})
		}
	}
	return out, nil
}

func word(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b >= 128
}

// Statements returns unquoted tokens grouped by real top-level boundaries.
// SQL-standard BEGIN ATOMIC and SQLite trigger bodies keep their internal semicolons.
func Statements(tokens []Token) [][]Token {
	var out [][]Token
	var current []Token
	depth := 0
	create := false
	routine := false
	trigger := false
	for _, t := range tokens {
		if t.Comment {
			continue
		}
		u := strings.ToUpper(t.Text)
		if len(current) == 0 {
			create = !t.Quoted && u == "CREATE"
			routine = false
			trigger = false
		}
		if !t.Quoted {
			if create && (u == "FUNCTION" || u == "PROCEDURE") {
				routine = true
			}
			if create && u == "TRIGGER" {
				trigger = true
			}
			if u == "CASE" || u == "BEGIN" && (routine || trigger) {
				depth++
			}
			if u == "END" && depth > 0 {
				depth--
			}
		}
		if u == ";" && !t.Quoted && depth == 0 {
			if len(current) > 0 {
				out = append(out, current)
			}
			current = nil
			continue
		}
		current = append(current, t)
	}
	if len(current) > 0 {
		out = append(out, current)
	}
	return out
}

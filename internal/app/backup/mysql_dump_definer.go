package backup

import (
	"regexp"
	"strings"
)

const mysqlDumpExecutableComment = `/\*(?:!|M!)[0-9]{5,6}[ \t]+`
const mysqlDumpCreateObject = `(?:CREATE|ALTER)(?:[ \t]+OR[ \t]+REPLACE)?(?:[ \t]+ALGORITHM[ \t]*=[ \t]*[a-z_]+)?`
const mysqlDumpAccountPart = `(?:\x60(?:[^\x60]|\x60\x60)*\x60|'(?:[^'\\]|\\.|'')*'|"(?:[^"\\]|\\.|"")*"|[a-z0-9_$%.:-]+)`

// mysqldump and mariadb-dump can wrap CREATE and DEFINER in separate
// executable comments. Match a DDL header, not an INSERT value or SQL
// SECURITY DEFINER. Keep the security mode and every other part of the DDL.
var mysqlDumpDefiner = regexp.MustCompile(`(?i)^([ \t]*(?:` +
	mysqlDumpCreateObject + `[ \t]+|(?:` + mysqlDumpExecutableComment + mysqlDumpCreateObject +
	`[ \t]*\*/[ \t]*)?` + mysqlDumpExecutableComment + `(?:` + mysqlDumpCreateObject + `[ \t]+)?))` +
	`DEFINER[ \t]*=[ \t]*` + mysqlDumpAccountPart + `[ \t]*@[ \t]*` + mysqlDumpAccountPart)

var mysqlDumpDatabaseDDL = regexp.MustCompile(`(?i)^(?:` + mysqlDumpExecutableComment + `)?(?:CREATE|DROP)[ \t]+DATABASE\b`)

type mysqlDumpFilter struct {
	delimiter string
	inObject  bool
}

func (f *mysqlDumpFilter) line(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	upper := strings.ToUpper(trimmed)
	if !f.inObject && strings.HasPrefix(upper, "DELIMITER ") {
		f.delimiter = strings.TrimSpace(trimmed[len("DELIMITER "):])
		return raw, true
	}
	if !f.inObject {
		if strings.HasPrefix(upper, "USE ") || strings.HasPrefix(upper, "-- CURRENT DATABASE:") ||
			mysqlDumpDatabaseDDL.MatchString(trimmed) {
			return "", false
		}
		if match := mysqlDumpDefiner.FindStringSubmatchIndex(raw); match != nil {
			raw = raw[:match[3]] + "DEFINER=CURRENT_USER" + raw[match[1]:]
		}
	}
	// Dump tools use a custom delimiter around stored object bodies. Do not
	// edit DDL-looking lines inside a body or inside its multiline strings.
	if f.delimiter != "" && f.delimiter != ";" {
		if strings.HasSuffix(trimmed, f.delimiter) {
			f.inObject = false
		} else if trimmed != "" && !strings.HasPrefix(trimmed, "--") {
			f.inObject = true
		}
	}
	return raw, true
}

package backup

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/antimage/antimage/internal/app/xrayconfig"
)

func portableMySQLSourceEngine(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if version, ok := strings.CutPrefix(line, "-- Server version"); ok {
			if strings.Contains(strings.ToLower(version), "mariadb") {
				return "mariadb", nil
			}
			return "mysql", nil
		}
		if line != "" && !strings.HasPrefix(line, "--") && !strings.HasPrefix(line, "/*") {
			break
		}
	}
	return "", scanner.Err()
}

var portableMySQLColumn = regexp.MustCompile("^  `([a-z_][a-z0-9_]*)` ")
var portableMySQLInsert = regexp.MustCompile("(?i)^INSERT INTO `([a-z_][a-z0-9_]*)` VALUES(?:[ \t]+|$)")

type portableNodeConfig struct {
	id     int64
	active bool
	raw    string
}

// preparePortableMySQLDump transforms native AntiMage dump output before the
// destination is changed. The restore command never sees source enrollments.
func preparePortableMySQLDump(inputPath, outputPath string) error {
	input, err := os.Open(inputPath)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(outputPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer output.Close()

	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 0, 1<<20), 64<<20)
	writer := bufio.NewWriter(output)
	filter := mysqlDumpFilter{}
	columns := map[string][]string{}
	var table string
	var configs []portableNodeConfig
	var master string
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line, keep := filter.line(scanner.Text())
		if !keep {
			continue
		}
		if !filter.inObject && (filter.delimiter == "" || filter.delimiter == ";") {
			switch {
			case strings.HasPrefix(line, "CREATE TABLE `"):
				table, _, _ = strings.Cut(line[len("CREATE TABLE `"):], "`")
			case strings.HasPrefix(line, ")"):
				table = ""
			case table == "nodes" || table == "xray_config":
				if match := portableMySQLColumn.FindStringSubmatch(line); match != nil {
					columns[table] = append(columns[table], match[1])
				}
			}
			match := portableMySQLInsert.FindStringSubmatchIndex(line)
			if match != nil {
				tableName := line[match[2]:match[3]]
				switch tableName {
				case "node_operations", "pending_node_certificates", "haproxy_targets", "vpn_user_sessions", "user_online_ips":
					if _, err := portableMySQLInsertValues(scanner, &lineNumber, line[match[1]:], false); err != nil {
						return err
					}
					continue
				case "nodes", "xray_config":
					literals, err := portableMySQLInsertValues(scanner, &lineNumber, line[match[1]:], true)
					if err != nil {
						return err
					}
					values, err := parsePortableMySQLRows(literals, columns[tableName])
					if err != nil {
						return fmt.Errorf("cannot read %s from the backup: %w", tableName, err)
					}
					for _, row := range values {
						id, err := strconv.ParseInt(row["id"], 10, 64)
						if err != nil || id <= 0 {
							return fmt.Errorf("invalid ID in backup table %s", tableName)
						}
						if tableName == "xray_config" {
							if id == 1 {
								master = row["data"]
							}
							continue
						}
						configs = append(configs, portableNodeConfig{id: id, active: row["status"] != "deleted", raw: row["xray_config"]})
						uplink, _ := strconv.ParseInt(row["uplink"], 10, 64)
						downlink, _ := strconv.ParseInt(row["downlink"], 10, 64)
						if _, err := fmt.Fprintf(writer, "INSERT INTO `nodes` (id,address,port,api_port,status,uplink,downlink) VALUES (%d,'',0,0,'deleted',%d,%d);\n", id, uplink, downlink); err != nil {
							return err
						}
					}
					if tableName == "nodes" {
						continue
					}
					line = line[:match[1]] + " " + literals
				}
			} else if portableMySQLUnexpectedWrite(line) {
				return fmt.Errorf("unsupported SQL data statement at dump line %d", lineNumber)
			}
		}
		if _, err := writer.WriteString(line + "\n"); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if len(columns["nodes"]) == 0 {
		return fmt.Errorf("backup does not contain a native AntiMage nodes table")
	}
	if err := writeDetachedMySQLInbounds(writer, master, configs); err != nil {
		return err
	}
	return writer.Flush()
}

func portableMySQLInsertValues(scanner *bufio.Scanner, lineNumber *int, first string, retain bool) (string, error) {
	var values strings.Builder
	for {
		part := strings.TrimSpace(first)
		if retain {
			if values.Len()+len(part) > 64<<20 {
				return "", fmt.Errorf("migration backup contains an oversized INSERT")
			}
			values.WriteString(part)
		}
		if strings.HasSuffix(part, ";") {
			return values.String(), nil
		}
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return "", err
			}
			return "", fmt.Errorf("unterminated INSERT in migration backup")
		}
		*lineNumber += 1
		first = scanner.Text()
	}
}

func portableMySQLUnexpectedWrite(line string) bool {
	upper := strings.ToUpper(strings.TrimSpace(line))
	for _, keyword := range []string{"INSERT ", "REPLACE ", "UPDATE ", "DELETE ", "LOAD DATA "} {
		if strings.HasPrefix(upper, keyword) {
			return true
		}
	}
	return false
}

// Native mysqldump rows contain only literals. Keep delimiters inside strings
// and decode the MySQL escapes before interpreting configuration JSON.
func parsePortableMySQLRows(input string, columns []string) ([]map[string]string, error) {
	var rows []map[string]string
	for strings.HasPrefix(input, "(") {
		input = input[1:]
		row := make(map[string]string, len(columns))
		for i, column := range columns {
			value, rest, err := parsePortableMySQLLiteral(input)
			if err != nil {
				return nil, err
			}
			row[column] = value
			delimiter := byte(',')
			if i == len(columns)-1 {
				delimiter = ')'
			}
			if len(rest) == 0 || rest[0] != delimiter {
				return nil, fmt.Errorf("dump row does not match table columns")
			}
			input = rest[1:]
		}
		rows = append(rows, row)
		if input == ";" {
			return rows, nil
		}
		if !strings.HasPrefix(input, ",(") {
			break
		}
		input = input[1:]
	}
	return nil, fmt.Errorf("unsupported dump row format")
}

func parsePortableMySQLLiteral(input string) (string, string, error) {
	if !strings.HasPrefix(input, "'") {
		end := strings.IndexAny(input, ",)")
		if end < 0 {
			return "", "", fmt.Errorf("unterminated SQL value")
		}
		value := input[:end]
		switch {
		case value == "NULL":
			value = ""
		case strings.HasPrefix(value, "0x"):
			raw, err := hex.DecodeString(value[2:])
			if err != nil {
				return "", "", fmt.Errorf("invalid hex SQL value")
			}
			value = string(raw)
		default:
			if _, err := strconv.ParseFloat(value, 64); err != nil {
				return "", "", fmt.Errorf("unsupported SQL literal")
			}
		}
		return value, input[end:], nil
	}

	var value strings.Builder
	for i := 1; i < len(input); i++ {
		c := input[i]
		switch c {
		case '\'':
			if i+1 >= len(input) || input[i+1] != '\'' {
				return value.String(), input[i+1:], nil
			}
			i++
		case '\\':
			i++
			if i == len(input) {
				return "", "", fmt.Errorf("unterminated SQL escape")
			}
			c = input[i]
			switch c {
			case '0':
				c = 0
			case 'b':
				c = '\b'
			case 'n':
				c = '\n'
			case 'r':
				c = '\r'
			case 't':
				c = '\t'
			case 'Z':
				c = 26
			}
		}
		value.WriteByte(c)
	}
	return "", "", fmt.Errorf("unterminated SQL string")
}

func writeDetachedMySQLInbounds(writer *bufio.Writer, master string, configs []portableNodeConfig) error {
	sort.Slice(configs, func(i, j int) bool {
		if configs[i].active != configs[j].active {
			return configs[i].active
		}
		return configs[i].id < configs[j].id
	})
	seen := make(map[string]string)
	const ddl = "CREATE TABLE IF NOT EXISTS detached_inbounds (tag VARCHAR(255) PRIMARY KEY, config LONGTEXT NOT NULL);\n"
	if _, err := writer.WriteString(ddl); err != nil {
		return err
	}
	for _, config := range append([]portableNodeConfig{{raw: master}}, configs...) {
		if config.raw == "" {
			continue
		}
		var payload struct {
			Inbounds []map[string]any `json:"inbounds"`
		}
		if err := json.Unmarshal([]byte(config.raw), &payload); err != nil {
			return fmt.Errorf("invalid Xray configuration in backup")
		}
		for _, inbound := range payload.Inbounds {
			if !xrayconfig.IsManageableInbound(inbound) {
				continue
			}
			tag, _ := inbound["tag"].(string)
			protocol, _ := inbound["protocol"].(string)
			key := strings.ToLower(tag)
			if existing, found := seen[key]; found {
				if !strings.EqualFold(existing, protocol) {
					return fmt.Errorf("inbound tag %q has conflicting protocols in the backup", tag)
				}
				continue
			}
			seen[key] = protocol
			if config.id == 0 {
				continue
			}
			raw, err := json.Marshal(inbound)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(writer, "INSERT INTO detached_inbounds (tag,config) VALUES (0x%s,0x%s);\n", hex.EncodeToString([]byte(tag)), hex.EncodeToString(raw)); err != nil {
				return err
			}
		}
	}
	return nil
}

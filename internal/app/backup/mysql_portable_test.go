package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPortableMySQLDumpDetachesNodesAndPreservesServiceLinks(t *testing.T) {
	input := "CREATE TABLE `nodes` (\n  `id` int NOT NULL,\n  `address` varchar(256),\n  `status` varchar(32),\n  `xray_config` text,\n  PRIMARY KEY (`id`)\n);\n" +
		"INSERT INTO `nodes` VALUES (7,'old.example','connected','{\"inbounds\":[{\"tag\":\"game\",\"protocol\":\"wireguard\",\"settings\":{\"label\":\"line\\\\nwith (comma,) and apostrophe\\'\"}}]}');\n" +
		"INSERT INTO `node_operations` VALUES\n(1,'source-operation'),\n(2,'second-operation');\n" +
		"INSERT INTO `service_hosts` VALUES (3,14,0);\n"
	dir := t.TempDir()
	source := filepath.Join(dir, "source.sql")
	destination := filepath.Join(dir, "portable.sql")
	if err := os.WriteFile(source, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	if err := preparePortableMySQLDump(source, destination); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	for _, want := range []string{"'deleted'", "detached_inbounds", "INSERT INTO `service_hosts` VALUES (3,14,0);"} {
		if !strings.Contains(out, want) {
			t.Errorf("portable dump missing %q", want)
		}
	}
	for _, forbidden := range []string{"old.example", "'connected'", "source-operation"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("portable dump contains %q", forbidden)
		}
	}
}

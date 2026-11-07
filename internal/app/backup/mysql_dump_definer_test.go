package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilterMySQLDumpRebindsStoredObjectDefiners(t *testing.T) {
	input := strings.Join([]string{
		"/*!40000 DROP DATABASE IF EXISTS `source`*/;",
		"CREATE DATABASE `source`;",
		"USE `source`;",
		"INSERT INTO `notes` VALUES ('CREATE DATABASE, DEFINER=`old`@`localhost`');",
		"/*!50001 CREATE ALGORITHM=UNDEFINED */",
		"/*!50013 DEFINER=`old`@`localhost` SQL SECURITY DEFINER */",
		"/*!50001 VIEW `v` AS SELECT 1 */;",
		"DELIMITER ;;",
		"/*!50003 CREATE*/ /*!50017 DEFINER=`old`@`%`*/ /*!50003 TRIGGER `tr` BEFORE INSERT ON `notes` FOR EACH ROW SET @x = 1 */;;",
		"CREATE DEFINER='old'@'localhost' PROCEDURE `p`()",
		"BEGIN",
		"  SELECT 'multiline",
		"CREATE DEFINER=`old`@`localhost` VIEW quoted_text",
		"  ';",
		"END;;",
		"/*M!50106 CREATE DEFINER=`a``b`@`localhost` EVENT `e` ON SCHEDULE EVERY 1 DAY DO SELECT 1 */;;",
		"DELIMITER ;",
	}, "\n")
	dir := t.TempDir()
	source, target := filepath.Join(dir, "source.sql"), filepath.Join(dir, "filtered.sql")
	if err := os.WriteFile(source, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	if err := filterMySQLDumpForDatabase(source, target); err != nil {
		t.Fatal(err)
	}
	filtered, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"INSERT INTO `notes` VALUES ('CREATE DATABASE, DEFINER=`old`@`localhost`');",
		"/*!50001 CREATE ALGORITHM=UNDEFINED */",
		"/*!50013 DEFINER=CURRENT_USER SQL SECURITY DEFINER */",
		"/*!50001 VIEW `v` AS SELECT 1 */;",
		"DELIMITER ;;",
		"/*!50003 CREATE*/ /*!50017 DEFINER=CURRENT_USER*/ /*!50003 TRIGGER `tr` BEFORE INSERT ON `notes` FOR EACH ROW SET @x = 1 */;;",
		"CREATE DEFINER=CURRENT_USER PROCEDURE `p`()",
		"BEGIN",
		"  SELECT 'multiline",
		"CREATE DEFINER=`old`@`localhost` VIEW quoted_text",
		"  ';",
		"END;;",
		"/*M!50106 CREATE DEFINER=CURRENT_USER EVENT `e` ON SCHEDULE EVERY 1 DAY DO SELECT 1 */;;",
		"DELIMITER ;",
		"",
	}, "\n")
	if string(filtered) != want {
		t.Fatalf("filtered dump:\n%s\nwant:\n%s", filtered, want)
	}
}

//go:build linux

package nodeagent

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var infrastructureInterfaceName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)

func parseInfrastructureSNMP(reader io.Reader) (map[string]uint64, error) {
	scanner := bufio.NewScanner(reader)
	result := make(map[string]uint64)
	for scanner.Scan() {
		header := strings.Fields(scanner.Text())
		if len(header) == 0 || header[0] != "Ip:" {
			continue
		}
		if !scanner.Scan() {
			return nil, errors.New("missing IP counter values")
		}
		values := strings.Fields(scanner.Text())
		if len(values) != len(header) || values[0] != "Ip:" {
			return nil, errors.New("invalid IP counter values")
		}
		for i, name := range header[1:] {
			if !strings.HasPrefix(name, "Reasm") && !strings.HasPrefix(name, "Frag") {
				continue
			}
			if name == "ReasmTimeout" {
				continue
			}
			value, err := strconv.ParseUint(values[i+1], 10, 64)
			if err != nil {
				return nil, err
			}
			result["ip."+name] = value
		}
	}
	return result, scanner.Err()
}

func parseInfrastructureSoftnet(reader io.Reader) (map[string]uint64, error) {
	scanner := bufio.NewScanner(reader)
	result := map[string]uint64{"softnet.dropped": 0, "softnet.time_squeeze": 0}
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			return nil, errors.New("invalid softnet row")
		}
		for index, name := range []string{"softnet.dropped", "softnet.time_squeeze"} {
			value, err := strconv.ParseUint(fields[index+1], 16, 64)
			if err != nil {
				return nil, err
			}
			if ^uint64(0)-result[name] < value {
				return nil, errors.New("softnet counter overflow")
			}
			result[name] += value
		}
	}
	return result, scanner.Err()
}

func readNetworkInfrastructure() (map[string]uint64, []networkInterfaceInfrastructure, error) {
	result := make(map[string]uint64)
	for path, parse := range map[string]func(io.Reader) (map[string]uint64, error){"/proc/net/snmp": parseInfrastructureSNMP, "/proc/net/softnet_stat": parseInfrastructureSoftnet} {
		file, err := os.Open(path)
		if err != nil {
			return nil, nil, err
		}
		values, err := parse(io.LimitReader(file, 1<<20))
		file.Close()
		if err != nil {
			return nil, nil, err
		}
		for name, value := range values {
			result[name] = value
		}
	}
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return nil, nil, err
	}
	interfaces := make([]networkInterfaceInfrastructure, 0, 32)
	for _, entry := range entries {
		name := entry.Name()
		if !infrastructureInterfaceName.MatchString(name) || len(interfaces) == 32 {
			continue
		}
		base := filepath.Join("/sys/class/net", name)
		mtu, err := readInfrastructureCounter(filepath.Join(base, "mtu"))
		if err != nil {
			continue
		}
		interfaces = append(interfaces, networkInterfaceInfrastructure{Name: name, MTU: mtu})
		for _, counter := range []string{"rx_dropped", "tx_dropped", "rx_errors", "tx_errors", "rx_bytes", "tx_bytes"} {
			if value, err := readInfrastructureCounter(filepath.Join(base, "statistics", counter)); err == nil {
				result["interface."+name+"."+counter] = value
			}
		}
	}
	return result, interfaces, nil
}

func readInfrastructureCounter(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
}

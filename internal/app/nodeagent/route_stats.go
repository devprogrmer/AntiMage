package nodeagent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

type xrayStatsQueryResponse struct {
	Stat []struct {
		Name  string          `json:"name"`
		Value json.RawMessage `json:"value"`
	} `json:"stat"`
}

func parseRouteOutboundStats(
	raw []byte,
) (string, []string, []*nodev1.RouteTestTraffic, error) {
	var response xrayStatsQueryResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return "", nil, nil, fmt.Errorf(
			"decode Xray stats response: %w",
			err,
		)
	}

	type traffic struct {
		up   int64
		down int64
	}

	byTag := make(map[string]*traffic)

	for _, stat := range response.Stat {
		tag, direction, ok := parseOutboundStatName(stat.Name)
		if !ok {
			continue
		}

		value, err := parseXrayStatValue(stat.Value)
		if err != nil {
			return "", nil, nil, fmt.Errorf(
				"decode Xray stat %q: %w",
				stat.Name,
				err,
			)
		}

		item := byTag[tag]
		if item == nil {
			item = &traffic{}
			byTag[tag] = item
		}

		switch direction {
		case "uplink":
			item.up += value
		case "downlink":
			item.down += value
		}
	}

	type ranked struct {
		tag   string
		up    int64
		down  int64
		total int64
	}

	active := make([]ranked, 0, len(byTag))

	for tag, item := range byTag {
		total := item.up + item.down
		if total <= 0 {
			continue
		}

		active = append(active, ranked{
			tag:   tag,
			up:    item.up,
			down:  item.down,
			total: total,
		})
	}

	sort.Slice(active, func(i, j int) bool {
		if active[i].total == active[j].total {
			return active[i].tag < active[j].tag
		}
		return active[i].total > active[j].total
	})

	if len(active) == 0 {
		return "", nil, nil, nil
	}

	trafficResult := make(
		[]*nodev1.RouteTestTraffic,
		0,
		len(active),
	)

	groupTags := make([]string, 0, len(active)-1)

	for index, item := range active {
		trafficResult = append(
			trafficResult,
			&nodev1.RouteTestTraffic{
				Tag:  item.tag,
				Up:   item.up,
				Down: item.down,
			},
		)

		if index > 0 {
			groupTags = append(groupTags, item.tag)
		}
	}

	return active[0].tag, groupTags, trafficResult, nil
}

func parseOutboundStatName(
	name string,
) (tag string, direction string, ok bool) {
	const prefix = "outbound>>>"

	if !strings.HasPrefix(name, prefix) {
		return "", "", false
	}

	rest := strings.TrimPrefix(name, prefix)
	parts := strings.Split(rest, ">>>")

	if len(parts) != 3 {
		return "", "", false
	}

	tag = strings.TrimSpace(parts[0])
	if tag == "" || parts[1] != "traffic" {
		return "", "", false
	}

	direction = strings.TrimSpace(parts[2])
	if direction != "uplink" && direction != "downlink" {
		return "", "", false
	}

	return tag, direction, true
}

func parseXrayStatValue(raw json.RawMessage) (int64, error) {
	text := strings.TrimSpace(string(raw))

	if text == "" || text == "null" {
		return 0, nil
	}

	if strings.HasPrefix(text, `"`) {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return 0, err
		}

		if strings.TrimSpace(value) == "" {
			return 0, nil
		}

		return strconv.ParseInt(
			strings.TrimSpace(value),
			10,
			64,
		)
	}

	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return 0, err
	}

	return strconv.ParseInt(number.String(), 10, 64)
}

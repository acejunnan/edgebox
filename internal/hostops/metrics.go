package hostops

import (
	"math"
	"strconv"
	"strings"
)

type usage struct {
	Total     uint64  `json:"total_bytes"`
	Used      uint64  `json:"used_bytes"`
	Available uint64  `json:"available_bytes"`
	Percent   float64 `json:"used_percent"`
}

func memoryUsage(text string) *usage {
	values := map[string]uint64{}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[2] == "kB" {
			n, err := strconv.ParseUint(fields[1], 10, 64)
			if err == nil && n <= ^uint64(0)/1024 {
				values[strings.TrimSuffix(fields[0], ":")] = n * 1024
			}
		}
	}
	total, available := values["MemTotal"], values["MemAvailable"]
	if _, ok := values["MemAvailable"]; !ok || total == 0 || available > total {
		return nil
	}
	return &usage{total, total - available, available, float64(total-available) * 100 / float64(total)}
}

func uptimeSeconds(text string) *float64 {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return nil
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || seconds < 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return nil
	}
	return &seconds
}

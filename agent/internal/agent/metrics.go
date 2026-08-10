package agent

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"syscall"

	"nodepanel/shared/proto"
)

// metricsSampler computes CPU% from deltas of /proc/stat.
type metricsSampler struct {
	lastBusy  uint64
	lastTotal uint64
	hasLast   bool
}

func (m *metricsSampler) sample() proto.MetricsData {
	md := proto.MetricsData{}
	busy, total := cpuSample()
	if m.hasLast && total > m.lastTotal {
		md.CPU = float64(busy-m.lastBusy) / float64(total-m.lastTotal) * 100
		if md.CPU < 0 {
			md.CPU = 0
		}
	}
	m.lastBusy, m.lastTotal, m.hasLast = busy, total, true

	if mt, ma, ok := memInfo(); ok {
		md.MemTotal = mt
		md.MemUsed = mt - ma
	}
	if t, u, ok := diskInfo("/"); ok {
		md.DiskTotal = t
		md.DiskUsed = u
	}
	md.Load1 = loadAvg()
	md.Uptime = uptime()
	return md
}

func cpuSample() (busy, total uint64) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if sc.Scan() {
		fields := strings.Fields(sc.Text()) // cpu  user nice system idle iowait ...
		if len(fields) >= 2 && fields[0] == "cpu" {
			var all uint64
			for _, fl := range fields[1:] {
				v, _ := strconv.ParseUint(fl, 10, 64)
				total += v
			}
			// idle = fields[4], iowait = fields[5] (if present)
			idle := uint64(0)
			if len(fields) > 4 {
				idle, _ = strconv.ParseUint(fields[4], 10, 64)
			}
			if len(fields) > 5 {
				iow, _ := strconv.ParseUint(fields[5], 10, 64)
				idle += iow
			}
			busy = total - idle
			_ = all
		}
	}
	return
}

func memInfo() (total, avail uint64, ok bool) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(parts[1], 10, 64)
		v *= 1024 // KiB -> bytes
		switch parts[0] {
		case "MemTotal:":
			total = v
		case "MemAvailable:":
			avail = v
		}
	}
	if total > 0 {
		ok = true
	}
	return
}

func diskInfo(path string) (total, used uint64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return
	}
	total = st.Blocks * uint64(st.Bsize)
	free := st.Bfree * uint64(st.Bsize)
	avail := st.Bavail * uint64(st.Bsize)
	used = total - free
	_ = avail
	return total, used, true
}

func loadAvg() float64 {
	f, err := os.Open("/proc/loadavg")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) > 0 {
			v, _ := strconv.ParseFloat(fields[0], 64)
			return v
		}
	}
	return 0
}

func uptime() int64 {
	f, err := os.Open("/proc/uptime")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) > 0 {
			v, _ := strconv.ParseFloat(fields[0], 64)
			return int64(v)
		}
	}
	return 0
}

package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// cst 中国标准时间（无夏令时，固定 +8，避免依赖容器 tzdata）
var cst = time.FixedZone("CST", 8*3600)

func icsEscape(s string) string {
	r := strings.NewReplacer(
		"\\", "\\\\",
		";", "\\;",
		",", "\\,",
		"\r\n", "\\n",
		"\n", "\\n",
		"\r", "",
	)
	return r.Replace(s)
}

// foldLine 按 RFC5545 折行（<=73 个 rune，续行以空格开头），rune-aware 避免切坏中文
func foldLine(line string) string {
	const max = 73
	if utf8.RuneCountInString(line) <= max {
		return line
	}
	var b strings.Builder
	n := 0
	for _, r := range line {
		if n >= max {
			b.WriteString("\r\n ")
			n = 0
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

func parseHM(s string) (int, int, bool) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	h, e1 := strconv.Atoi(parts[0])
	m, e2 := strconv.Atoi(parts[1])
	if e1 != nil || e2 != nil {
		return 0, 0, false
	}
	return h, m, true
}

func termStart(week, weekday int) time.Time {
	now := time.Now().In(cst)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, cst)
	// 当前周本周一 - (week-1) 周 = 第一周周一
	return today.AddDate(0, 0, -((week-1)*7 + (weekday - 1)))
}

func buildICS(keys *keysFile) (string, error) {
	pr, err := getPortalWeek(keys)
	if err != nil {
		return "", err
	}
	xq, _ := strconv.Atoi(pr.Data.XQ)
	week := weekForDate(pr, nowCST())
	if week < 1 {
		week = 1
	}
	weekCount, _ := strconv.Atoi(pr.Data.WeekCount)
	if weekCount < 1 {
		weekCount = 18
	}
	wd, _ := strconv.Atoi(pr.Data.XQJ)
	if wd < 1 || wd > 7 {
		wd = 1
	}

	kb, err := getSchedule(keys, pr.Data.XN, xqmOf(xq))
	if err != nil {
		return "", err
	}
	times, _ := getTimes(keys, pr.Data.XN, xqmOf(xq))

	start := termStart(week, wd)
	nowStamp := time.Now().In(cst).UTC().Format("20060102T150405Z")

	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\n")
	b.WriteString("VERSION:2.0\r\n")
	b.WriteString("PRODID:-//gpnu-schudle//timetable//CN\r\n")
	b.WriteString("CALSCALE:GREGORIAN\r\n")
	b.WriteString("METHOD:PUBLISH\r\n")
	b.WriteString("X-WR-CALNAME:广技师课表\r\n")
	b.WriteString("X-WR-TIMEZONE:Asia/Shanghai\r\n")

	for _, k := range kb.KbList {
		xqj, err := strconv.Atoi(k.XQJ)
		if err != nil || xqj < 1 || xqj > 7 {
			continue
		}
		a, z := periodRange(k.JC)
		ts, ok1 := times[a]
		te, ok2 := times[z]
		if !ok1 || !ok2 {
			continue
		}
		sh, sm, ok := parseHM(ts[0])
		if !ok {
			continue
		}
		eh, em, ok := parseHM(te[1])
		if !ok {
			continue
		}
		weeks := weeksOf(k.ZCD)
		for w := 1; w <= weekCount; w++ {
			if !weeks[w] {
				continue
			}
			d := start.AddDate(0, 0, (w-1)*7+(xqj-1))
			st := time.Date(d.Year(), d.Month(), d.Day(), sh, sm, 0, 0, cst).UTC()
			en := time.Date(d.Year(), d.Month(), d.Day(), eh, em, 0, 0, cst).UTC()
			if !en.After(st) {
				continue
			}
			uid := fmt.Sprintf("%s-%s-%02d%02d@gpnu-schudle", k.KCH, d.Format("20060102"), sh, sm)
			sum := k.KCMC
			if k.XSLXBJ != "" {
				sum += strings.TrimSpace(k.XSLXBJ)
			}
			loc := k.CDMC
			if k.LH != "" && !strings.Contains(loc, strings.TrimSuffix(k.LH, "（白云校区）")) {
				loc = k.LH + " " + loc
			}
			desc := fmt.Sprintf("教师: %s\\n教学班: %s\\n周次: %s\\n学分: %s",
				icsEscape(k.XM), icsEscape(k.JXBMC), icsEscape(k.ZCD), icsEscape(k.XF))

			b.WriteString("BEGIN:VEVENT\r\n")
			b.WriteString(foldLine("UID:"+uid) + "\r\n")
			b.WriteString("DTSTAMP:" + nowStamp + "\r\n")
			b.WriteString("DTSTART:" + st.Format("20060102T150405Z") + "\r\n")
			b.WriteString("DTEND:" + en.Format("20060102T150405Z") + "\r\n")
			b.WriteString(foldLine("SUMMARY:"+icsEscape(sum)) + "\r\n")
			b.WriteString(foldLine("LOCATION:"+icsEscape(loc)) + "\r\n")
			b.WriteString(foldLine("DESCRIPTION:"+desc) + "\r\n")
			b.WriteString("END:VEVENT\r\n")
		}
	}

	b.WriteString("END:VCALENDAR\r\n")
	return b.String(), nil
}

func icsHandler(keysPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result, err := withKeysRetry(keysPath, func(keys *keysFile) (any, error) {
			return buildICS(keys)
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		ics, _ := result.(string)
		w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
		w.Header().Set("Content-Disposition", `inline; filename="gpnu.ics"`)
		w.Header().Set("Cache-Control", "no-cache")
		w.Write([]byte(ics))
	}
}

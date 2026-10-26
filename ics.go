package main

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
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

var (
	// 去掉 “(白云)” “（白云校区）” 这类校区标注
	campusParenRe  = regexp.MustCompile(`[（(][^（()）]*(?:校区|白云|大学城|本部)[^（()）]*[）)]`)
	campusPrefixRe = regexp.MustCompile(`^[\p{Han}]{0,4}校区`)
	roomTailRe     = regexp.MustCompile(`^(.*?)([A-Za-z]?\d{1,4}[A-Za-z]?)$`)
)

// cleanLocation 去掉校区标注、空白、结尾“室”
func cleanLocation(loc string) string {
	s := strings.TrimSpace(loc)
	if s == "" {
		return ""
	}
	s = campusParenRe.ReplaceAllString(s, "")
	s = campusPrefixRe.ReplaceAllString(s, "")
	s = strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\u3000', '\t', '\n', '\r':
			return -1
		}
		return r
	}, s)
	return strings.TrimSuffix(s, "室")
}

// classroomTag 从场地名中提取「教学楼+门牌号」，如 "一教503(白云)" -> "一教503"。
// 校区信息一律丢弃；无法可靠识别时返回空串（静默失败）。
func classroomTag(loc string) string {
	s := cleanLocation(loc)
	if s == "" {
		return ""
	}

	m := roomTailRe.FindStringSubmatch(s)
	if m == nil || m[2] == "" {
		return ""
	}
	building := strings.Trim(m[1], "-－—–_")
	if building != "" {
		if utf8.RuneCountInString(building) > 12 {
			return ""
		}
		// 楼名至少含一个文字（汉字/字母），排除纯符号噪声
		if !strings.ContainsFunc(building, unicode.IsLetter) {
			return ""
		}
	}
	return s
}

// buildingName 从楼号中取出纯楼名（去掉房号与校区）
func buildingName(loc string) string {
	s := cleanLocation(loc)
	if s == "" {
		return ""
	}
	if m := roomTailRe.FindStringSubmatch(s); m != nil && m[2] != "" {
		s = strings.Trim(m[1], "-－—–_")
	}
	if s == "" || utf8.RuneCountInString(s) > 12 {
		return ""
	}
	if !strings.ContainsFunc(s, unicode.IsLetter) {
		return ""
	}
	return s
}

// classroomTagOf 优先用场地名；若场地名只有房号，则用楼号补全
func classroomTagOf(k kbItem) string {
	tag := classroomTag(k.CDMC)
	if tag == "" {
		return ""
	}
	if r, _ := utf8.DecodeRuneInString(tag); !unicode.IsDigit(r) {
		return tag
	}
	if building := buildingName(k.LH); building != "" {
		return building + tag
	}
	return tag
}

func buildICSFromData(data *scheduleData) string {
	week := computeWeek(data.zs, nowCST())
	if week < 1 {
		week = 1
	}
	weekCount := data.weekCount
	if weekCount < 1 {
		weekCount = 18
	}
	start := icsTermStart(week, data.xqj)
	nowStamp := time.Now().In(cst).UTC().Format("20060102T150405Z")

	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\n")
	b.WriteString("VERSION:2.0\r\n")
	b.WriteString("PRODID:-//gpnu-schudle//timetable//CN\r\n")
	b.WriteString("CALSCALE:GREGORIAN\r\n")
	b.WriteString("METHOD:PUBLISH\r\n")
	b.WriteString("X-WR-CALNAME:广技师课表\r\n")
	b.WriteString("X-WR-TIMEZONE:Asia/Shanghai\r\n")

	for _, k := range data.kb.KbList {
		xqj, err := strconv.Atoi(k.XQJ)
		if err != nil || xqj < 1 || xqj > 7 {
			continue
		}
		a, z := periodRange(k.JC)
		ts, ok1 := data.times[a]
		te, ok2 := data.times[z]
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
			sum := strings.TrimSpace(k.KCMC)
			if tag := classroomTagOf(k); tag != "" {
				sum += " " + tag
			}
			loc := classroomTagOf(k)
			if loc == "" {
				loc = cleanLocation(k.CDMC)
			}
			if loc == "" {
				loc = k.CDMC
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
	return b.String()
}

// icsTermStart 优先用配置的学期起始日，否则退回门户周次反推
func icsTermStart(week, xqj int) time.Time {
	if start, ok := configuredTermStart(); ok {
		return start
	}
	return termStart(week, xqj)
}

func icsHandler(keysPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := scheduleForEnv(keysPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		ics := buildICSFromData(data)
		w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
		w.Header().Set("Content-Disposition", `inline; filename="gpnu.ics"`)
		w.Header().Set("Cache-Control", "no-cache")
		w.Write([]byte(ics))
	}
}

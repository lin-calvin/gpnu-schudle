package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	portalCard = "9446b0bdeef14c13aeb8cd60e9816ab3"
	portalURL  = "https://portal.gpnu.edu.cn/api/uppcard/kbsz/queryAWeekSchedule"
	kbURL      = "https://jwglxt.gpnu.edu.cn/jwglxt/kbcx/xskbcx_cxXsgrkb.html?gnmkdm=N2151"
	rjcURL     = "https://jwglxt.gpnu.edu.cn/jwglxt/kbcx/xskbcx_cxRjc.html?gnmkdm=N2151"
	campusID   = "5" // 白云校区
)

// ---------- keys.json ----------

type keysFile struct {
	Browser string            `json:"browser"`
	Cookies map[string]string `json:"cookies"` // "portal" / "jwglxt" -> Cookie header
}

func loadKeys(path string) (*keysFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var k keysFile
	if err := json.Unmarshal(b, &k); err != nil {
		return nil, err
	}
	if k.Cookies["portal"] == "" || k.Cookies["jwglxt"] == "" {
		return nil, errors.New("keys.json 缺少 portal 或 jwglxt cookie，请重跑 dump_keys.py")
	}
	return &k, nil
}

// ---------- 上游接口数据结构 ----------

type portalResp struct {
	Meta struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	} `json:"meta"`
	Data struct {
		XN        string `json:"xn"`
		XQ        string `json:"xq"`
		ZS        string `json:"zs"`
		XQJ       string `json:"xqj"`
		WeekCount string `json:"weekcount"`
	} `json:"data"`
}

type kbItem struct {
	KCMC   string `json:"kcmc"`
	XM     string `json:"xm"`
	CDMC   string `json:"cdmc"`
	LH     string `json:"lh"`
	JC     string `json:"jc"`
	XQJ    string `json:"xqj"`
	ZCD    string `json:"zcd"`
	KCH    string `json:"kch"`
	KCLB   string `json:"kclb"`
	XF     string `json:"xf"`
	JXBMC  string `json:"jxbmc"`
	JXBZC  string `json:"jxbzc"`
	XSLXBJ string `json:"xslxbj"`
	KHFSMC string `json:"khfsmc"`
}

type sjkItem struct {
	KCMC   string `json:"kcmc"`
	JSXM   string `json:"jsxm"`
	QSJSZ  string `json:"qsjsz"`
	SJKCGS string `json:"sjkcgs"`
}

type kbResp struct {
	Xsxx struct {
		XH   string `json:"XH"`
		XM   string `json:"XM"`
		BJMC string `json:"BJMC"`
		XNMC string `json:"XNMC"`
	} `json:"xsxx"`
	KbList  []kbItem  `json:"kbList"`
	SjkList []sjkItem `json:"sjkList"`
}

type rjcItem struct {
	JCMC  string `json:"jcmc"`
	QSSJ  string `json:"qssj"`
	JSSJ  string `json:"jssj"`
	RSDMC string `json:"rsdmc"`
}

// ---------- 输出给前端的数据结构 ----------

type Class struct {
	Name     string `json:"name"`
	Teacher  string `json:"teacher"`
	Room     string `json:"room"`
	Building string `json:"building"`
	Periods  string `json:"periods"`
	Start    string `json:"start"`
	End      string `json:"end"`
	Weeks    string `json:"weeks"`
	Type     string `json:"type"`
	Credit   string `json:"credit"`
	Exam     string `json:"exam"`
}

type Day struct {
	Weekday int     `json:"weekday"`
	Name    string  `json:"name"`
	Classes []Class `json:"classes"`
}

type TodayResp struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Student struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Class string `json:"class"`
	} `json:"student"`
	Term struct {
		XN        string `json:"xn"`
		XQ        int    `json:"xq"`
		Name      string `json:"name"`
		WeekCount int    `json:"weekCount"`
	} `json:"term"`
	Date        string  `json:"date"`
	Weekday     int     `json:"weekday"`
	WeekdayName string  `json:"weekdayName"`
	Week        int     `json:"week"`
	IsToday     bool    `json:"isToday"`
	Classes     []Class `json:"classes"`
}

type WeekResp struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Week  int    `json:"week"`
	Term  string `json:"term"`
	Days  []Day  `json:"days"`
}

// ---------- 上游请求 ----------

var httpClient = &http.Client{Timeout: 15 * time.Second}

func getPortalWeek(keys *keysFile) (*portalResp, error) {
	q := url.Values{}
	q.Set("cardId", portalCard)
	q.Set("results", "")
	q.Set("years", "")
	req, _ := http.NewRequest("GET", portalURL+"?"+q.Encode(), nil)
	req.Header.Set("Cookie", keys.Cookies["portal"])
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var pr portalResp
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, errors.New("门户返回非 JSON，可能登录失效")
	}
	if !pr.Meta.Success {
		return nil, fmt.Errorf("门户: %s", pr.Meta.Message)
	}
	return &pr, nil
}

func postForm(u, cookie string, form url.Values) ([]byte, error) {
	req, _ := http.NewRequest("POST", u, strings.NewReader(form.Encode()))
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func getSchedule(keys *keysFile, xn, xqm string) (*kbResp, error) {
	form := url.Values{}
	form.Set("xnm", xn)
	form.Set("xqm", xqm)
	form.Set("kzlx", "ck")
	body, err := postForm(kbURL, keys.Cookies["jwglxt"], form)
	if err != nil {
		return nil, err
	}
	var kb kbResp
	if err := json.Unmarshal(body, &kb); err != nil {
		return nil, errors.New("教务返回非 JSON，会话失效，请重跑 dump_keys.py")
	}
	return &kb, nil
}

func getTimes(keys *keysFile, xn, xqm string) (map[int][2]string, error) {
	form := url.Values{}
	form.Set("xnm", xn)
	form.Set("xqm", xqm)
	form.Set("xqh_id", campusID)
	body, err := postForm(rjcURL, keys.Cookies["jwglxt"], form)
	if err != nil {
		return nil, err
	}
	var items []rjcItem
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, nil // 时间拿不到不致命
	}
	m := map[int][2]string{}
	for _, it := range items {
		if n, err := strconv.Atoi(strings.TrimSpace(it.JCMC)); err == nil {
			m[n] = [2]string{it.QSSJ, it.JSSJ}
		}
	}
	return m, nil
}

// ---------- 解析 ----------

var (
	weekRe = regexp.MustCompile(`(?:第)?(\d+)(?:-(\d+))?周`)
	numRe  = regexp.MustCompile(`\d+`)
)

func weeksOf(zcd string) map[int]bool {
	out := map[int]bool{}
	for _, m := range weekRe.FindAllStringSubmatch(zcd, -1) {
		a, _ := strconv.Atoi(m[1])
		b := a
		if m[2] != "" {
			b, _ = strconv.Atoi(m[2])
		}
		if a > b {
			a, b = b, a
		}
		for i := a; i <= b; i++ {
			out[i] = true
		}
	}
	return out
}

func periodRange(jc string) (int, int) {
	nums := numRe.FindAllString(jc, -1)
	if len(nums) == 0 {
		return 0, 0
	}
	a, _ := strconv.Atoi(nums[0])
	b := a
	if len(nums) > 1 {
		b, _ = strconv.Atoi(nums[len(nums)-1])
	}
	return a, b
}

func toClass(k kbItem, times map[int][2]string) Class {
	a, b := periodRange(k.JC)
	start, end := "", ""
	if t, ok := times[a]; ok {
		start = t[0]
	}
	if t, ok := times[b]; ok {
		end = t[1]
	}
	return Class{
		Name: k.KCMC, Teacher: k.XM, Room: k.CDMC, Building: k.LH,
		Periods: k.JC, Start: start, End: end, Weeks: k.ZCD,
		Type: strings.TrimSpace(k.XSLXBJ), Credit: k.XF, Exam: k.KHFSMC,
	}
}

func parseCourses(kb *kbResp, times map[int][2]string, week int) map[int][]Class {
	byDay := map[int][]Class{}
	for _, k := range kb.KbList {
		wd, err := strconv.Atoi(k.XQJ)
		if err != nil || wd < 1 || wd > 7 {
			continue
		}
		if !weeksOf(k.ZCD)[week] {
			continue
		}
		byDay[wd] = append(byDay[wd], toClass(k, times))
	}
	for wd := range byDay {
		cs := byDay[wd]
		sort.Slice(cs, func(i, j int) bool {
			ai, _ := periodRange(cs[i].Periods)
			aj, _ := periodRange(cs[j].Periods)
			return ai < aj
		})
		byDay[wd] = cs
	}
	return byDay
}

func xqmOf(xq int) string {
	switch xq {
	case 2:
		return "12"
	case 3:
		return "16"
	default:
		return "3"
	}
}

func weekdayName(wd int) string {
	return [...]string{"", "周一", "周二", "周三", "周四", "周五", "周六", "周日"}[wd]
}

// nowCST 统一用固定 +8 时区，容器无 tzdata 时 time.Local 会是 UTC，不能依赖
func nowCST() time.Time {
	return time.Now().In(cst)
}

func configuredTermStart() (time.Time, bool) {
	value := os.Getenv("TERM_START_DATE")
	if value == "" {
		return time.Time{}, false
	}
	date, err := time.ParseInLocation("2006-01-02", value, cst)
	if err != nil {
		return time.Time{}, false
	}
	return time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, cst), true
}

func weekForDate(pr *portalResp, date time.Time) int {
	if start, ok := configuredTermStart(); ok {
		current := time.Date(date.In(cst).Year(), date.In(cst).Month(), date.In(cst).Day(), 0, 0, 0, 0, cst)
		weeks := int(current.Sub(start).Hours() / (24 * 7))
		if weeks >= 0 {
			return weeks + 1
		}
	}
	week, _ := strconv.Atoi(pr.Data.ZS)
	if week < 1 {
		return 1
	}
	return week
}

// jwWeekday 把 Go 的 weekday(周日=0) 转成正方的 1..7(周一=1)
func jwWeekday(t time.Time) int {
	if t.Weekday() == time.Sunday {
		return 7
	}
	return int(t.Weekday())
}

// ---------- HTTP 处理 ----------

type apiFn func(url.Values) (any, error)

// withKeysRetry 会话中途失效时清掉缓存重新登录一次，避免缓存期内整段不可用
func withKeysRetry(keysPath string, fn func(*keysFile) (any, error)) (any, error) {
	keys, err := authenticatedKeys(keysPath)
	if err != nil {
		return nil, err
	}
	result, err := fn(keys)
	if err == nil || !autoLoginEnabled() {
		return result, err
	}
	invalidateAuth()
	keys, err = authenticatedKeys(keysPath)
	if err != nil {
		return nil, err
	}
	return fn(keys)
}

func makeTodayHandler(keysPath string) apiFn {
	return func(q url.Values) (any, error) {
		return withKeysRetry(keysPath, func(keys *keysFile) (any, error) {
			return buildToday(keys, q)
		})
	}
}

func buildToday(keys *keysFile, q url.Values) (any, error) {
	pr, err := getPortalWeek(keys)
	if err != nil {
		return nil, err
	}
	xq, _ := strconv.Atoi(pr.Data.XQ)
	weekCount, _ := strconv.Atoi(pr.Data.WeekCount)

	day := nowCST()
	if s := q.Get("date"); s != "" {
		if t, err := time.ParseInLocation("2006-01-02", s, cst); err == nil {
			day = t
		}
	}
	week := weekForDate(pr, day)
	wd := jwWeekday(day)

	kb, err := getSchedule(keys, pr.Data.XN, xqmOf(xq))
	if err != nil {
		return nil, err
	}
	times, _ := getTimes(keys, pr.Data.XN, xqmOf(xq))
	byDay := parseCourses(kb, times, week)

	resp := TodayResp{
		OK: true, Date: day.Format("2006-01-02"), Weekday: wd,
		WeekdayName: weekdayName(wd), Week: week,
		IsToday: day.Format("2006-01-02") == nowCST().Format("2006-01-02"),
		Classes: byDay[wd],
	}
	resp.Student.ID = kb.Xsxx.XH
	resp.Student.Name = kb.Xsxx.XM
	resp.Student.Class = kb.Xsxx.BJMC
	resp.Term.XN = pr.Data.XN
	resp.Term.XQ = xq
	resp.Term.Name = pr.Data.XN + " 第" + strconv.Itoa(xq) + "学期"
	resp.Term.WeekCount = weekCount
	if resp.Classes == nil {
		resp.Classes = []Class{}
	}
	return resp, nil
}

func makeWeekHandler(keysPath string) apiFn {
	return func(q url.Values) (any, error) {
		return withKeysRetry(keysPath, func(keys *keysFile) (any, error) {
			return buildWeek(keys, q)
		})
	}
}

func buildWeek(keys *keysFile, q url.Values) (any, error) {
	pr, err := getPortalWeek(keys)
	if err != nil {
		return nil, err
	}
	xq, _ := strconv.Atoi(pr.Data.XQ)
	week := weekForDate(pr, nowCST())
	if s := q.Get("week"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			week = n
		}
	}
	kb, err := getSchedule(keys, pr.Data.XN, xqmOf(xq))
	if err != nil {
		return nil, err
	}
	times, _ := getTimes(keys, pr.Data.XN, xqmOf(xq))
	byDay := parseCourses(kb, times, week)

	resp := WeekResp{OK: true, Week: week,
		Term: pr.Data.XN + " 第" + strconv.Itoa(xq) + "学期"}
	for wd := 1; wd <= 7; wd++ {
		cs := byDay[wd]
		if cs == nil {
			cs = []Class{}
		}
		resp.Days = append(resp.Days, Day{Weekday: wd, Name: weekdayName(wd), Classes: cs})
	}
	return resp, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// corsOrigin 允许的跨域来源，默认 * （接口不带凭据，任意站点可读；靠网络边界限制访问）
var corsOrigin = "*"

func apiHandler(fn apiFn) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", corsOrigin)
		w.Header().Set("Vary", "Origin")
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		v, err := fn(r.URL.Query())
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, v)
	}
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8787", "listen address")
	keysDefault := os.Getenv("KEYS_FILE")
	if keysDefault == "" {
		keysDefault = "keys.json"
	}
	keysPath := flag.String("keys", keysDefault, "path to keys.json")
	flag.Parse()

	if v := os.Getenv("CORS_ORIGIN"); v != "" {
		corsOrigin = v
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PROPFIND" {
			// 让客户端从根路径也能发现 principal
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(http.StatusMultiStatus)
			io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>`+"\n"+
				`<d:multistatus xmlns:d="DAV:"><d:response><d:href>/</d:href><d:propstat><d:prop>`+
				`<d:current-user-principal><d:href>`+caldavBase+`</d:href></d:current-user-principal>`+
				`<d:resourcetype><d:collection/></d:resourcetype>`+
				`</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Write([]byte(`{"service":"gpnu-schudle","endpoints":["/ics","/api/ics","/caldav/","/api/today","/api/week"]}`))
	})
	mux.HandleFunc("/caldav", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, caldavBase, http.StatusMovedPermanently)
	})
	mux.HandleFunc(caldavBase, caldavHandler(*keysPath))
	mux.HandleFunc("/.well-known/caldav", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, caldavBase, http.StatusMovedPermanently)
	})
	mux.HandleFunc("/api/today", apiHandler(makeTodayHandler(*keysPath)))
	mux.HandleFunc("/api/week", apiHandler(makeWeekHandler(*keysPath)))
	mux.HandleFunc("/ics", icsHandler(*keysPath))
	mux.HandleFunc("/api/ics", icsHandler(*keysPath))

	log.Printf("gpnu-schudle listening on %s (keys=%s)", *addr, *keysPath)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

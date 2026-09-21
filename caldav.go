package main

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// 只读 CalDAV：单个日历集合，内容由现有 ICS 生成逻辑提供。
// 支持 OPTIONS / PROPFIND / REPORT(calendar-query, calendar-multiget) / GET。
// PUBLIC 设置时用 Basic 里的学号/密码登录教务，否则回退环境变量单用户。
const (
	caldavBase     = "/caldav/"
	caldavObject   = "/caldav/timetable.ics"
	caldavName     = "广技师课表"
	davNamespace   = "DAV:"
	caldavNS       = "urn:ietf:params:xml:ns:caldav"
	caldavServerNS = "http://calendarserver.org/ns/"
)

func caldavPublic() bool {
	return os.Getenv("PUBLIC") != ""
}

func caldavETag(ics string) string {
	sum := sha1.Sum([]byte(ics))
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

// caldavKeys 返回本次请求要用的会话；public 模式下还返回学号密码用于失效重登
func caldavKeys(keysPath string, r *http.Request) (*keysFile, string, string, error) {
	if !caldavPublic() {
		result, err := withKeysRetry(keysPath, func(keys *keysFile) (any, error) {
			return keys, nil
		})
		if err != nil {
			return nil, "", "", err
		}
		keys, _ := result.(*keysFile)
		return keys, "", "", nil
	}

	username, password, ok := r.BasicAuth()
	if !ok || username == "" {
		return nil, "", "", errAuthFailed
	}
	keys, err := sessionForCredentials(username, password)
	if err != nil {
		return nil, "", "", err
	}
	return keys, username, password, nil
}

func caldavICS(keys *keysFile, username, password string) (string, string, error) {
	ics, err := buildICS(keys)
	if err != nil && caldavPublic() && username != "" {
		// 缓存的会话可能已失效，清掉后用同一凭据重新登录一次
		invalidateUserSession(username, password)
		fresh, loginErr := sessionForCredentials(username, password)
		if loginErr == nil {
			ics, err = buildICS(fresh)
		}
	}
	if err != nil {
		return "", "", err
	}
	return ics, caldavETag(ics), nil
}

func caldavHandler(keysPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.Header().Set("DAV", "1, 3, calendar-access")
			w.Header().Set("Allow", "OPTIONS, GET, HEAD, PROPFIND, REPORT")
			w.Header().Set("MS-Author-Via", "DAV")
			w.WriteHeader(http.StatusOK)
			return
		}

		keys, username, password, err := caldavKeys(keysPath, r)
		if err != nil {
			if errors.Is(err, errAuthFailed) {
				w.Header().Set("WWW-Authenticate", `Basic realm="gpnu-timetable"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		ics, etag, err := caldavICS(keys, username, password)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		switch r.Method {
		case http.MethodGet, http.MethodHead:
			w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
			w.Header().Set("ETag", etag)
			if r.Method == http.MethodGet {
				io.WriteString(w, ics)
			}
		case "PROPFIND":
			writeCaldavPropfind(w, r, ics, etag)
		case "REPORT":
			writeCaldavReport(w, r, ics, etag)
		default:
			w.Header().Set("Allow", "OPTIONS, GET, HEAD, PROPFIND, REPORT")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func caldavIsObject(path string) bool {
	return strings.HasSuffix(strings.TrimRight(path, "/"), ".ics")
}

func caldavCollectionProps(ics, etag string) string {
	return strings.Join([]string{
		"<d:resourcetype><d:collection/><c:calendar/></d:resourcetype>",
		"<d:displayname>" + caldavName + "</d:displayname>",
		"<d:current-user-principal><d:href>" + caldavBase + "</d:href></d:current-user-principal>",
		"<c:calendar-home-set><d:href>" + caldavBase + "</d:href></c:calendar-home-set>",
		`<c:supported-calendar-component-set><c:comp name="VEVENT"/></c:supported-calendar-component-set>`,
		"<c:calendar-description>" + caldavName + "</c:calendar-description>",
		"<d:getcontenttype>text/calendar; charset=utf-8</d:getcontenttype>",
		"<d:getetag>" + etag + "</d:getetag>",
		"<cs:getctag>" + etag + "</cs:getctag>",
		"<d:supported-report-set>" +
			"<d:supported-report><d:report><c:calendar-query/></d:report></d:supported-report>" +
			"<d:supported-report><d:report><c:calendar-multiget/></d:report></d:supported-report>" +
			"</d:supported-report-set>",
	}, "")
}

func caldavObjectProps(etag string) string {
	return strings.Join([]string{
		"<d:resourcetype/>",
		"<d:getcontenttype>text/calendar; charset=utf-8</d:getcontenttype>",
		"<d:getetag>" + etag + "</d:getetag>",
	}, "")
}

func caldavResponse(href, props string) string {
	return "<d:response><d:href>" + href + "</d:href>" +
		"<d:propstat><d:prop>" + props + "</d:prop>" +
		"<d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>"
}

func writeCaldavPropfind(w http.ResponseWriter, r *http.Request, ics, etag string) {
	href := r.URL.Path
	if !strings.HasPrefix(href, caldavBase) {
		href = caldavBase
	}
	var responses []string
	if caldavIsObject(href) {
		responses = append(responses, caldavResponse(caldavObject, caldavObjectProps(etag)))
	} else {
		responses = append(responses, caldavResponse(caldavBase, caldavCollectionProps(ics, etag)))
		if strings.EqualFold(r.Header.Get("Depth"), "1") {
			responses = append(responses, caldavResponse(caldavObject, caldavObjectProps(etag)))
		}
	}
	writeCaldavMultiStatus(w, responses)
}

func writeCaldavReport(w http.ResponseWriter, r *http.Request, ics, etag string) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	query := string(body)
	if !strings.Contains(query, "calendar-multiget") && !strings.Contains(query, "calendar-query") {
		http.Error(w, "unsupported report", http.StatusForbidden)
		return
	}
	calendarData := "<c:calendar-data>" + xmlEscape(ics) + "</c:calendar-data>"
	props := caldavObjectProps(etag) + calendarData
	writeCaldavMultiStatus(w, []string{caldavResponse(caldavObject, props)})
}

func writeCaldavMultiStatus(w http.ResponseWriter, responses []string) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>`+"\n")
	io.WriteString(w, fmt.Sprintf(
		`<d:multistatus xmlns:d="%s" xmlns:c="%s" xmlns:cs="%s">%s</d:multistatus>`,
		davNamespace, caldavNS, caldavServerNS, strings.Join(responses, ""),
	))
}

func xmlEscape(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
	)
	return replacer.Replace(s)
}

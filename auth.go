package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// errAuthFailed 表示教务认证失败（学号/密码错），用于区分 401 与上游故障
var errAuthFailed = errors.New("教务认证失败")

const (
	casBase       = "https://cas.gpnu.edu.cn"
	portalService = "https://portal.gpnu.edu.cn/shiro-cas"
	jwglxtService = "https://jwglxt.gpnu.edu.cn/sso/lyiotlogin"
	casAppJS      = casBase + "/assets/js/app.523338dac63532ba3269.js"
	openAIBaseURL = "https://openrouter.ai/api/v1"
	defaultModel  = "inclusionai/ling-3.0-flash-vl:free"
	userAgent     = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/132 Safari/537.36"
)

type casCaptcha struct {
	UID     string `json:"uid"`
	Content string `json:"content"`
}

type casLoginResponse struct {
	Meta struct {
		Success bool `json:"success"`
	} `json:"meta"`
	Data struct {
		Code   string `json:"code"`
		TGT    string `json:"tgt"`
		Ticket string `json:"ticket"`
	} `json:"data"`
	TGT    string `json:"tgt"`
	Ticket string `json:"ticket"`
}

func (r *casLoginResponse) normalize() {
	if r.Data.TGT == "" {
		r.Data.TGT = r.TGT
	}
	if r.Data.Ticket == "" {
		r.Data.Ticket = r.Ticket
	}
}

type openAIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error json.RawMessage `json:"error"`
}

var authState struct {
	sync.Mutex
	keys   *keysFile
	expiry time.Time
}

func autoLoginEnabled() bool {
	return os.Getenv("CAS_USERNAME") != "" &&
		os.Getenv("CAS_PASSWORD") != "" &&
		os.Getenv("OPENAI_API_KEY") != ""
}

func openAIEndpoint() string {
	base := os.Getenv("OPENAI_BASE_URL")
	if base == "" {
		base = openAIBaseURL
	}
	return strings.TrimRight(base, "/") + "/chat/completions"
}

func authenticatedKeys(path string) (*keysFile, error) {
	if !autoLoginEnabled() {
		return loadKeys(path)
	}

	authState.Lock()
	defer authState.Unlock()
	if authState.keys != nil && time.Now().Before(authState.expiry) {
		return authState.keys, nil
	}

	keys, err := loginWithoutBrowser()
	if err != nil {
		return nil, err
	}
	authState.keys = keys
	authState.expiry = time.Now().Add(3*time.Hour + 30*time.Minute)
	return keys, nil
}

// invalidateAuth 丢弃内存中的会话，下次请求会重新登录
func invalidateAuth() {
	authState.Lock()
	authState.keys = nil
	authState.expiry = time.Time{}
	authState.Unlock()
}

// ---------- CalDAV 每用户会话缓存（PUBLIC 模式） ----------

type userSession struct {
	keys     *keysFile
	expiry   time.Time
	lastUsed time.Time
}

var userSessionCache = struct {
	sync.Mutex
	entries map[string]userSession
}{entries: map[string]userSession{}}

const (
	userSessionTTL = 3*time.Hour + 30*time.Minute
	userSessionMax = 200
)

// sessionKey = sha256(username || 0 || password)
// 必须包含密码，否则知道学号即可命中他人缓存造成认证绕过
func sessionKey(username, password string) string {
	sum := sha256.Sum256([]byte(username + "\x00" + password))
	return hex.EncodeToString(sum[:])
}

func sessionForCredentials(username, password string) (*keysFile, error) {
	key := sessionKey(username, password)
	now := time.Now()

	userSessionCache.Lock()
	if entry, ok := userSessionCache.entries[key]; ok && now.Before(entry.expiry) {
		entry.lastUsed = now
		userSessionCache.entries[key] = entry
		userSessionCache.Unlock()
		return entry.keys, nil
	}
	userSessionCache.Unlock()

	keys, err := loginWithCredentials(username, password)
	if err != nil {
		return nil, err
	}

	userSessionCache.Lock()
	userSessionCache.entries[key] = userSession{keys: keys, expiry: now.Add(userSessionTTL), lastUsed: now}
	evictUserSessionsLocked()
	userSessionCache.Unlock()
	return keys, nil
}

func invalidateUserSession(username, password string) {
	key := sessionKey(username, password)
	userSessionCache.Lock()
	delete(userSessionCache.entries, key)
	userSessionCache.Unlock()
}

func evictUserSessionsLocked() {
	if len(userSessionCache.entries) <= userSessionMax {
		return
	}
	now := time.Now()
	for key, entry := range userSessionCache.entries {
		if now.After(entry.expiry) {
			delete(userSessionCache.entries, key)
		}
	}
	for len(userSessionCache.entries) > userSessionMax {
		var oldestKey string
		var oldest time.Time
		first := true
		for key, entry := range userSessionCache.entries {
			if first || entry.lastUsed.Before(oldest) {
				oldest = entry.lastUsed
				oldestKey = key
				first = false
			}
		}
		delete(userSessionCache.entries, oldestKey)
	}
}

func newAuthClient() (*http.Client, *cookiejar.Jar, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, nil, err
	}
	return &http.Client{
		Timeout: 30 * time.Second,
		Jar:     jar,
	}, jar, nil
}

func authRequest(client *http.Client, method, rawURL string, values url.Values) (*http.Response, error) {
	var body io.Reader
	if values != nil {
		body = strings.NewReader(values.Encode())
	}

	req, err := http.NewRequest(method, rawURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	if values != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	}
	return client.Do(req)
}

func readAuthResponse(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

func rsaPublicKey(js string) (*big.Int, *big.Int, error) {
	modulus := regexp.MustCompile(`modulus:\\?"([0-9a-fA-F]+)`).FindStringSubmatch(js)
	exponent := regexp.MustCompile(`public_exponent:\\?"([0-9a-fA-F]+)`).FindStringSubmatch(js)
	if len(modulus) != 2 || len(exponent) != 2 {
		return nil, nil, errors.New("CAS RSA 公钥格式变化")
	}

	n, ok := new(big.Int).SetString(modulus[1], 16)
	if !ok {
		return nil, nil, errors.New("CAS RSA 模数无效")
	}
	e, ok := new(big.Int).SetString(exponent[1], 16)
	if !ok {
		return nil, nil, errors.New("CAS RSA 指数无效")
	}
	return n, e, nil
}

func encryptCASPassword(password string, modulus, exponent *big.Int) string {
	value := new(big.Int).SetBytes(reverseBytes([]byte(password)))
	value.Exp(value, exponent, modulus)
	size := (modulus.BitLen() + 7) / 8
	return fmt.Sprintf("%0*x", size*2, value)
}

func reverseBytes(data []byte) []byte {
	result := make([]byte, len(data))
	for i := range data {
		result[len(data)-1-i] = data[i]
	}
	return result
}

func modelCaptchaAnswer(imageData string) (string, error) {
	model := os.Getenv("OPENAI_MODEL")
	if model == "" {
		model = defaultModel
	}

	payload := map[string]any{
		"model": model,
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]string{
						"type": "text",
						"text": "Read this arithmetic captcha. Return only the integer answer. Do not include reasoning.",
					},
					map[string]any{
						"type":      "image_url",
						"image_url": map[string]string{"url": imageData},
					},
				},
			},
		},
		"temperature": 0,
		"max_tokens":  64,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodPost, openAIEndpoint(), strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("OPENAI_API_KEY"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HTTP-Referer", "https://jwglxt.gpnu.edu.cn")
	req.Header.Set("X-Title", "GPNU timetable captcha")

	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	data, err := readAuthResponse(resp)
	if err != nil {
		return "", err
	}

	var result openAIResponse
	if err := json.Unmarshal(data, &result); err != nil {
		return "", errors.New("模型返回非 JSON")
	}
	if len(result.Choices) == 0 {
		return "", errors.New("模型暂不可用")
	}

	answer := strings.TrimSpace(result.Choices[0].Message.Content)
	if !regexp.MustCompile(`^\d{1,3}$`).MatchString(answer) {
		return "", errors.New("模型没有返回单独数字")
	}
	return answer, nil
}

func casTicket(client *http.Client, modulus, exponent *big.Int, captcha casCaptcha, answer, username, password, service string) (casLoginResponse, error) {
	var result casLoginResponse
	resp, err := authRequest(client, http.MethodPost, casBase+"/lyuapServer/v1/tickets", url.Values{
		"username":  {username},
		"password":  {encryptCASPassword(password, modulus, exponent)},
		"service":   {service},
		"loginType": {""},
		"id":        {captcha.UID},
		"code":      {answer},
	})
	if err != nil {
		return result, err
	}
	body, err := readAuthResponse(resp)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return result, errors.New("CAS 返回非 JSON")
	}
	result.normalize()
	return result, nil
}

func serviceTicket(client *http.Client, tgt, service string) (string, error) {
	resp, err := authRequest(client, http.MethodPost, casBase+"/lyuapServer/v1/tickets/"+url.PathEscape(tgt), url.Values{
		"service":    {service},
		"loginToken": {"loginToken"},
	})
	if err != nil {
		return "", err
	}
	body, err := readAuthResponse(resp)
	if err != nil {
		return "", err
	}

	value := strings.TrimSpace(string(body))
	if separator := strings.IndexByte(value, ';'); separator >= 0 {
		value = value[:separator]
	}
	if value == "" || strings.Contains(value, "NOAUTHORIZATION") || strings.Contains(value, "NOREGISTER") {
		return "", errors.New("CAS service ticket 无效")
	}
	return value, nil
}

func visitService(client *http.Client, service, ticket string) error {
	target, err := url.Parse(service)
	if err != nil {
		return err
	}
	query := target.Query()
	query.Set("ticket", ticket)
	target.RawQuery = query.Encode()

	resp, err := client.Get(target.String())
	if err != nil {
		return err
	}
	_, err = readAuthResponse(resp)
	return err
}

func loginWithoutBrowser() (*keysFile, error) {
	return loginWithCredentials(os.Getenv("CAS_USERNAME"), os.Getenv("CAS_PASSWORD"))
}

func loginWithCredentials(username, password string) (*keysFile, error) {
	client, jar, err := newAuthClient()
	if err != nil {
		return nil, err
	}

	resp, err := client.Get(casBase + "/lyuapServer/login?service=" + url.QueryEscape(portalService))
	if err != nil {
		return nil, err
	}
	if _, err := readAuthResponse(resp); err != nil {
		return nil, err
	}

	resp, err = client.Get(casAppJS)
	if err != nil {
		return nil, err
	}
	js, err := readAuthResponse(resp)
	if err != nil {
		return nil, err
	}
	modulus, exponent, err := rsaPublicKey(string(js))
	if err != nil {
		return nil, err
	}

	var login casLoginResponse
	var lastError string
	for attempt := 1; attempt <= 5; attempt++ {
		resp, err = client.Get(casBase + "/lyuapServer/kaptcha?uid=")
		if err != nil {
			return nil, err
		}
		body, err := readAuthResponse(resp)
		if err != nil {
			return nil, err
		}

		var captcha casCaptcha
		if err := json.Unmarshal(body, &captcha); err != nil {
			return nil, errors.New("验证码响应无效")
		}
		answer, err := modelCaptchaAnswer(captcha.Content)
		if err != nil {
			lastError = err.Error()
			continue
		}

		login, err = casTicket(client, modulus, exponent, captcha, answer, username, password, portalService)
		if err != nil {
			lastError = err.Error()
			continue
		}
		if login.Data.Code == "CODEFALSE" {
			lastError = "验证码错误"
			continue
		}
		if login.Data.Code == "PASSERROR" || login.Data.Code == "NOUSER" {
			return nil, fmt.Errorf("%w: %s", errAuthFailed, login.Data.Code)
		}
		if login.Data.TGT == "" || login.Data.Ticket == "" {
			lastError = login.Data.Code
			continue
		}
		break
	}
	if login.Data.TGT == "" || login.Data.Ticket == "" {
		return nil, fmt.Errorf("CAS 登录失败(最多5次): %s", lastError)
	}

	if err := visitService(client, portalService, login.Data.Ticket); err != nil {
		return nil, err
	}
	jwTicket, err := serviceTicket(client, login.Data.TGT, jwglxtService)
	if err != nil {
		return nil, err
	}
	if err := visitService(client, jwglxtService, jwTicket); err != nil {
		return nil, err
	}

	portalURL, _ := url.Parse("https://portal.gpnu.edu.cn/")
	jwglxtURL, _ := url.Parse("https://jwglxt.gpnu.edu.cn/jwglxt/kbcx/xskbcx_cxXsgrkb.html")
	portalCookie := cookieHeader(jar.Cookies(portalURL))
	jwglxtCookie := cookieHeader(jar.Cookies(jwglxtURL))
	if portalCookie == "" || jwglxtCookie == "" {
		return nil, errors.New("SSO 成功但没有拿到目标站 Cookie")
	}
	return &keysFile{Cookies: map[string]string{
		"portal": portalCookie,
		"jwglxt": jwglxtCookie,
	}}, nil
}

func cookieHeader(cookies []*http.Cookie) string {
	parts := make([]string, 0, len(cookies))
	for _, cookie := range cookies {
		parts = append(parts, cookie.Name+"="+cookie.Value)
	}
	return strings.Join(parts, "; ")
}

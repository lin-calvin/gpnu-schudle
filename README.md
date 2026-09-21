# gpnu-schudle

广东技术师范大学（GPNU）教务课表小服务：把正方教务系统的课表，变成 **JSON API / ICS 订阅 / CalDAV**。

> GPNU 专用，站点参数写死在代码里，适配其它学校见文末「站点适配」。

---

## Docker 部署

### 构建镜像

```bash
git clone <repo> gpnu-schudle && cd gpnu-schudle
docker build -t gpnu-schudle .
```

### 运行

```bash
docker run -d --name gpnu-schudle \
  -p 8080:8080 \
  -e CAS_USERNAME=202600000000000 \
  -e CAS_PASSWORD='你的教务密码' \
  -e OPENAI_API_KEY='sk-...' \
  -e OPENAI_BASE_URL=https://openrouter.ai/api/v1 \
  -e OPENAI_MODEL=inclusionai/ling-3.0-flash-vl:free \
  -e TERM_START_DATE=2026-09-07 \
  -e PUBLIC=1 \
  gpnu-schudle
```

访问 `http://localhost:8080/` 返回服务信息即成功。

### docker compose

```yaml
services:
  gpnu-schudle:
    image: gpnu-schudle
    build: .
    restart: unless-stopped
    ports:
      - "8080:8080"
    environment:
      CAS_USERNAME: "202600000000000"
      CAS_PASSWORD: "你的教务密码"
      OPENAI_API_KEY: "sk-..."
      OPENAI_BASE_URL: "https://openrouter.ai/api/v1"
      OPENAI_MODEL: "inclusionai/ling-3.0-flash-vl:free"
      TERM_START_DATE: "2026-09-07"
      PUBLIC: "1"
```

```bash
docker compose up -d
```

### 环境变量

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `CAS_USERNAME` | 空 | 学号（环境变量单用户，用于 `/ics`、`/api/*`） |
| `CAS_PASSWORD` | 空 | 教务密码 |
| `OPENAI_API_KEY` | 空 | 验证码识别用的多模态模型 Key |
| `OPENAI_BASE_URL` | `https://openrouter.ai/api/v1` | OpenAI 兼容端点 |
| `OPENAI_MODEL` | `inclusionai/ling-3.0-flash-vl:free` | 多模态模型名 |
| `TERM_START_DATE` | 空 | 学期第一周周一（`YYYY-MM-DD`），用于计算周次 |
| `PUBLIC` | 空 | 设置后 CalDAV 启用每用户 Basic 认证 |
| `CORS_ORIGIN` | `*` | API 允许的跨域来源 |
| `TZ` | 系统 | 建议 `Asia/Shanghai` |

> 只需要 ICS/JSON、不需要每用户 CalDAV 时，可以不设 `PUBLIC`。
> 无需持久化：会话只缓存在内存，容器重启后自动重新登录。

---

## 端点

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/` | 服务信息 JSON |
| GET | `/api/today` | 今日课程（`?date=YYYY-MM-DD`、`?week=N`） |
| GET | `/api/week` | 整周课表（`?week=N`） |
| GET | `/ics`、`/api/ics` | ICS 订阅（环境变量单用户） |
| GET | `/caldav/`、`/caldav/timetable.ics` | CalDAV（`PUBLIC` 模式 Basic 认证） |
| GET | `/.well-known/caldav` | 重定向到 `/caldav/` |

## 客户端配置

ICS 订阅：

```
https://<你的域名>/ics
```

CalDAV（需 `PUBLIC=1`，用户名密码即教务系统的学号与密码）：

```
服务器: https://<你的域名>/caldav/
用户名: 学号
密码:   教务密码
```

---

## 站点适配

GPNU 参数写死在代码中，适配其它学校需修改：

- `auth.go`：`casBase`、`portalService`、`jwglxtService`、`casAppJS`（CAS 前端 JS，含哈希文件名，学校升级后可能变化）
- `main.go`：`portalURL`、`portalCard`（门户课表卡片 id）、`kbURL`、`rjcURL`、`campusID`（校区 id）、`xqmOf`（学期编码）
- `ics.go`：`PRODID`、ICS 文件名、`X-WR-CALNAME`

## 免责声明

本项目仅供学习与参考，请勿用于任何未授权用途。

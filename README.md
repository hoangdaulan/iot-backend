# MyIoT Backend

Go + Gin REST API and MQTT bridge for the MyIoT system: **one ESP32** with three sensors (temperature, humidity, light) and one LED. PostgreSQL stores the data; the Flutter web app (`iot-frontend`) is the client.

```
Flutter ──REST/JWT──▶ Gin handlers ─▶ services ─▶ repositories ─▶ PostgreSQL
                                        ▲   │
ESP32 ◀──MQTT──▶ Mosquitto ◀──MQTT──▶ mqtt client (ingest sensor data, LED commands)
```

## Quick start

```sh
docker compose up --build     # PostgreSQL :5432, Mosquitto :1883, API :8080
curl localhost:8080/health    # {"status":"ok"}
docker compose down           # stop (add -v to delete the database volume)
```

The API container applies the migrations and the development seeds on start.

**Development accounts** (password `123456` for both):

| Username | Email | Role |
|---|---|---|
| `admin` | admin@myiot.local | ADMIN |
| `user01` | user01@myiot.local | USER |

Run the Flutter app against it from `iot-frontend`:

```sh
fvm flutter run -d chrome --dart-define=BASE_URL=http://localhost:8080
```

### Without Docker

Needs PostgreSQL and an MQTT broker. Copy `.env.example` to `.env`, adjust it, then:

```sh
set -a; . ./.env; set +a
go run ./cmd/server
```

## Project layout

```
cmd/server/            entry point: config, DB, MQTT, HTTP, graceful shutdown
internal/config/       environment variables
internal/model/        User, Device, Sensor, SensorData, DeviceAction
internal/dto/          JSON contracts (match the Flutter DTOs)
internal/apperr/       errors carrying an HTTP status and {"message"}
internal/repository/   interfaces; postgres/ (pgx, SQL filtering and paging) and memory/ (tests)
internal/service/      auth + JWT, sensors (incl. MQTT ingestion), device commands + history
internal/handler/      Gin handlers
internal/middleware/   JWT auth, CORS
internal/router/       routes
internal/mqtt/         paho client, payload parsing
internal/database/     connection, embedded migration and seed runner
migrations/            schema + the single device and three sensors
seeds/                 development users, a week of sensor data, 40 commands
tests/                 HTTP API tests; PostgreSQL and MQTT integration tests
```

## Database

| Table | Columns |
|---|---|
| `users` | id, name, email (unique), password (bcrypt), username (unique), phone, avatar, github, figma, role (`ADMIN`/`USER`), created_at, updated_at |
| `devices` | id, name, type, status (`ON`/`OFF`), mqtt_topic, created_at, updated_at |
| `sensors` | id, name, type (unique: `temperature`/`humidity`/`light`), unit, status, mqtt_topic, created_at, updated_at |
| `sensor_data` | id, sensor_id → sensors, value, timestamp. Indexes: `sensor_id`, `timestamp`, `(sensor_id, timestamp DESC)` |
| `device_actions` | id, user_id → users, device_id → devices, action (`TURN_ON`/`TURN_OFF`), result (`PENDING`/`SUCCESS`/`FAILED`/`TIMEOUT`), message, timestamp, completed_at |

The migration creates the only device (`id 1`, `ESP32`, type `LED`, status `OFF`) and the three sensors (`1` Temperature °C, `2` Humidity %, `3` Light lux). There is no CRUD for devices, sensors or users beyond registration and profile editing.

## REST API

Errors are `{"message": "..."}` with 400, 401, 403, 404, 409, 500 or 504. Every endpoint except login and register needs `Authorization: Bearer <accessToken>`; a missing, invalid or expired token gives **401**. There are no refresh tokens: an expired token (default lifetime 24 h) means logging in again.

| Method | Path | Notes |
|---|---|---|
| POST | `/api/auth/login` | `{username, password}` (username or email) → `{accessToken, user: {id, username, name?, role}}`; 401 `Invalid email or password` |
| POST | `/api/auth/register` | `{username, email, password}` → 201 `{message, user}`; 409 if taken; 400 if invalid (username ≥ 3, password 6–72 bytes, valid email) |
| GET | `/api/auth/profile` | full user, never the password |
| PATCH | `/api/auth/profile` | any of `{name, phone, avatar, github, figma}`; absent fields unchanged |
| PATCH | `/api/auth/password` | `{oldPassword, newPassword}` → 204; wrong old password is **400** (not 401, which would end the session) |
| GET | `/api/sensors` | the three sensors |
| GET | `/api/sensor-data/latest` | `{data: {temperature\|humidity\|light: {id, value, unit, timestamp}}, deviceStatus: "ON"\|"OFF"}`; a type is absent until it has data |
| GET | `/api/sensor-data/history` | query `type`, `timeRange`, `value`, `page`, `size` → `{data: {temperature: [...], humidity: [...], light: [...]}, page, pageSize, totalElements, totalPages}` |
| POST | `/api/devices/1/command` | `{command: "ON"\|"OFF"}` → `{deviceId, command, status, message}`: **200** for `SUCCESS`/`FAILED`, **504** for `TIMEOUT`; other ids → 404 `Device not found` |
| GET | `/api/devices/control-history` | query `from`, `to`, `page`, `size` (optional `deviceId`) → `{content: [{id, deviceId, deviceName, action, status?, result, timestamp, message?}], page, size, totalElements, totalPages}` |
| GET | `/health` | liveness |

**Paging** is 0-based everywhere: `size` defaults to 20 and must be 1–5000. Sensor history keeps the report's grouped-by-type body; its `page`/`pageSize`/`totalElements`/`totalPages` count individual measurements, newest first.

**Time parameters** are ISO-8601. `timeRange` is an interval, `<from>/<to>`, where `..` or empty means open-ended, e.g. `2026-08-14T00:00:00Z/..`. Times without a zone are read as UTC. Responses use UTC.

## MQTT contract (ESP32 firmware)

No firmware existed when this was written, so **this is the contract the firmware must implement**. Topics are configurable.

| Topic (default) | Direction | Payload |
|---|---|---|
| `myiot/esp32/sensor-data` | ESP32 → backend | `{"temperature": 28.7, "humidity": 60.5, "light": 420}` |
| `myiot/esp32/device-control` | backend → ESP32 | `{"actionId": 42, "command": "ON"}` |
| `myiot/esp32/device-response` | ESP32 → backend | `{"actionId": 42, "status": "SUCCESS", "state": "ON", "message": "optional"}` |

- **Sensor data.** Every field is optional, and an optional `"timestamp"` (RFC 3339) replaces the receive time. Each valid value becomes one `sensor_data` row for the matching seeded sensor. Values outside the hardware range (temperature −40…125, humidity 0…100, light 0…65535) are dropped individually. Non-JSON payloads, or payloads with no valid value, are logged and ignored. Sensors are never created from MQTT.
- **Commands.** The API saves a `PENDING` action, publishes the command, and waits up to `MQTT_COMMAND_TIMEOUT` (default 5 s) for a response. Commands are serialized, since there is one LED.
- **Responses.** `status` is `SUCCESS` or `FAILED`. `state` (`ON`/`OFF`) is optional and becomes the stored device status on success. `actionId` should echo the command; if it's omitted, the response applies to the command in flight. Responses for other or already timed-out commands are ignored.
- On `SUCCESS` the device status is updated; on `FAILED` or a timeout it isn't. The action is completed with the result and message either way.
- **`GET /api/sensor-data/latest`** returns the latest persisted values. There is no on-demand read request to the ESP32.

The client reconnects automatically and re-subscribes after every reconnect. When the broker is down the API keeps serving, and commands return `FAILED` (`Could not reach the MQTT broker`).

Try it without hardware, using the broker container's CLI tools:

```sh
# Send sensor values
docker compose exec mosquitto mosquitto_pub -t myiot/esp32/sensor-data \
  -m '{"temperature": 29.4, "humidity": 58.2, "light": 512}'
# Answer the next LED command as the ESP32 would
docker compose exec mosquitto sh -c 'msg=$(mosquitto_sub -t myiot/esp32/device-control -C 1);
  id=$(echo "$msg" | sed -E "s/.*\"actionId\":([0-9]+).*/\1/");
  mosquitto_pub -t myiot/esp32/device-response -m "{\"actionId\":$id,\"status\":\"SUCCESS\",\"state\":\"ON\"}"'
```

## Configuration

| Variable | Default | |
|---|---|---|
| `HTTP_PORT` | `8080` | |
| `DATABASE_URL` | `postgres://myiot:myiot@localhost:5432/myiot?sslmode=disable` | |
| `JWT_SECRET` | — | **required** |
| `JWT_TTL` | `24h` | access-token lifetime |
| `CORS_ALLOWED_ORIGINS` | `*` | comma-separated, or `*` |
| `RUN_MIGRATIONS` | `true` | |
| `RUN_SEEDS` | `false` | development data (compose sets `true`) |
| `MQTT_ENABLED` | `true` | |
| `MQTT_BROKER` / `MQTT_PORT` | `localhost` / `1883` | |
| `MQTT_USERNAME` / `MQTT_PASSWORD` | empty | |
| `MQTT_CLIENT_ID` | `myiot-backend` | must be unique on the broker |
| `MQTT_QOS` | `1` | 0, 1 or 2 |
| `MQTT_TOPIC_SENSOR_DATA` | `myiot/esp32/sensor-data` | |
| `MQTT_TOPIC_DEVICE_CONTROL` | `myiot/esp32/device-control` | |
| `MQTT_TOPIC_DEVICE_RESPONSE` | `myiot/esp32/device-response` | |
| `MQTT_COMMAND_TIMEOUT` | `5s` | wait for the ESP32 before answering 504 |

The development broker (`mosquitto/mosquitto.conf`) allows anonymous access. Configure credentials before exposing it.

## Tests

```sh
go fmt ./... && go vet ./...
go test ./...          # unit + HTTP API tests (in-memory repositories, fake ESP32)
go test -race ./...
```

The integration tests run against real services and are skipped unless configured. **They drop and recreate the `public` schema**, so use throwaway containers:

```sh
docker run -d --rm --name myiot-test-pg -e POSTGRES_USER=test -e POSTGRES_PASSWORD=test \
  -e POSTGRES_DB=myiot_test -p 55432:5432 postgres:17-alpine
docker run -d --rm --name myiot-test-mqtt -p 51883:1883 \
  -v "$PWD/mosquitto/mosquitto.conf:/mosquitto/config/mosquitto.conf:ro" eclipse-mosquitto:2
TEST_DATABASE_URL='postgres://test:test@localhost:55432/myiot_test?sslmode=disable' \
TEST_MQTT_BROKER=localhost:51883 go test -race ./tests/
docker stop myiot-test-pg myiot-test-mqtt
```

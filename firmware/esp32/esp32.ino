// ESP32 firmware for the MyIoT backend.
//
// Publishes {"temperature":27.3,"humidity":55.0,"lux":2.5,"wind_speed":342} to TOPIC_SENSOR
// (fields whose sensor failed are left out) and executes LED commands from TOPIC_CONTROL:
//
//   backend -> ESP32   {"actionId": 42, "deviceId": 2, "command": "ON"}   ("ON" or "OFF")
//   ESP32 -> backend   {"actionId": 42, "status": "SUCCESS", "state": "ON", "message": "LED 2 on"}
//
// deviceId N (1..LED_COUNT) is the backend's "LED N" device and drives LED_PINS[N - 1].
//
// wind_speed is simulated: a random whole number from 0 to 500 km/h every SENSOR_INTERVAL_MS.
// LED 3 (device 3, "LED WIND") is the wind warning light. It lights up by itself while the wind
// speed is >= WIND_WARNING_THRESHOLD and is off otherwise; commands for it are refused.
//
// Libraries: PubSubClient, BH1750, DHT sensor library, ArduinoJson (v7).

#include <WiFi.h>
#include <PubSubClient.h>
#include <Wire.h>
#include <BH1750.h>
#include <DHT.h>
#include <ArduinoJson.h>

#include "secrets.h"

// Must match the backend's MQTT_TOPIC_* settings (these are its defaults).
const char* TOPIC_SENSOR = "myiot/esp32/sensor-data";
const char* TOPIC_CONTROL = "myiot/esp32/device-control";
const char* TOPIC_RESPONSE = "myiot/esp32/device-response";

#define DHT_PIN 22
#define DHT_TYPE DHT11
#define SDA_PIN 33
#define SCL_PIN 25

const uint8_t LED_PINS[] = {23, 21, 16};
const size_t LED_COUNT = sizeof(LED_PINS) / sizeof(LED_PINS[0]);

// The wind warning LED is the third LED (device 3). Keep the threshold equal to the backend's.
const long WIND_LED_DEVICE_ID = 3;
const int WIND_MAX = 500;
const int WIND_WARNING_THRESHOLD = 300;

const unsigned long SENSOR_INTERVAL_MS = 2000;
const unsigned long WIFI_RETRY_MS = 5000;
const unsigned long MQTT_RETRY_MS = 2000;

DHT dht(DHT_PIN, DHT_TYPE);
BH1750 lightMeter;
bool bh1750Ready = false;

WiFiClient espClient;
PubSubClient client(espClient);

unsigned long lastSensorTime = 0;
unsigned long lastWifiTry = 0;
unsigned long lastMqttTry = 0;

// deviceId is 1-based; returns -1 when it does not match an LED.
int ledPin(long deviceId) {
  if (deviceId < 1 || deviceId > (long)LED_COUNT) return -1;
  return LED_PINS[deviceId - 1];
}

void publishResponse(long actionId, int pin, const char* status, const char* message) {
  JsonDocument doc;
  doc["actionId"] = actionId;
  doc["status"] = status;
  if (pin >= 0) doc["state"] = digitalRead(pin) == HIGH ? "ON" : "OFF";
  doc["message"] = message;

  char buf[200];
  size_t n = serializeJson(doc, buf, sizeof(buf));
  client.publish(TOPIC_RESPONSE, reinterpret_cast<const uint8_t*>(buf), n, false);

  Serial.print("RESP -> ");
  Serial.println(buf);
}

void handleControl(const byte* payload, unsigned int length) {
  JsonDocument doc;
  DeserializationError err = deserializeJson(doc, payload, length);
  long actionId = doc["actionId"] | 0L;

  if (err) {
    Serial.print("Bad control JSON: ");
    Serial.println(err.c_str());
    publishResponse(actionId, -1, "FAILED", "Invalid control message");
    return;
  }

  long deviceId = doc["deviceId"] | 0L;
  int pin = ledPin(deviceId);
  if (pin < 0) {
    publishResponse(actionId, -1, "FAILED", "Unknown device");
    return;
  }

  const char* command = doc["command"] | "";
  char message[24];
  if (strcmp(command, "ON") == 0) {
    digitalWrite(pin, HIGH);
    snprintf(message, sizeof(message), "LED %ld on", deviceId);
    publishResponse(actionId, pin, "SUCCESS", message);
  } else if (strcmp(command, "OFF") == 0) {
    digitalWrite(pin, LOW);
    snprintf(message, sizeof(message), "LED %ld off", deviceId);
    publishResponse(actionId, pin, "SUCCESS", message);
  } else {
    publishResponse(actionId, pin, "FAILED", "Unknown command");
  }
}

void mqttCallback(char* topic, byte* payload, unsigned int length) {
  Serial.print("MQTT <- ");
  Serial.print(topic);
  Serial.print(": ");
  Serial.write(payload, length);
  Serial.println();

  if (strcmp(topic, TOPIC_CONTROL) == 0) {
    handleControl(payload, length);
  }
}

// Non-blocking: called from loop(), retries at most every WIFI_RETRY_MS.
void ensureWiFi() {
  if (WiFi.status() == WL_CONNECTED) return;
  unsigned long now = millis();
  if (lastWifiTry != 0 && now - lastWifiTry < WIFI_RETRY_MS) return;
  lastWifiTry = now;
  Serial.println("Connecting WiFi...");
  WiFi.disconnect();
  WiFi.begin(WIFI_SSID, WIFI_PASSWORD);
}

void ensureMqtt() {
  if (client.connected() || WiFi.status() != WL_CONNECTED) return;
  unsigned long now = millis();
  if (lastMqttTry != 0 && now - lastMqttTry < MQTT_RETRY_MS) return;
  lastMqttTry = now;

  String clientId = "esp32-" + String((uint32_t)ESP.getEfuseMac(), HEX);
  Serial.print("Connecting MQTT... ");
  if (client.connect(clientId.c_str(), MQTT_USER, MQTT_PASS)) {
    Serial.println("connected");
    client.subscribe(TOPIC_CONTROL, 1);
  } else {
    Serial.print("failed, rc=");
    Serial.println(client.state());
  }
}

// Draws the simulated wind speed and lights the warning LED when it reaches the threshold.
// Runs every interval, even while MQTT is down, so the LED never depends on the network.
int updateWindWarning() {
  int wind = random(0, WIND_MAX + 1);  // 0 to 500 inclusive
  bool warning = wind >= WIND_WARNING_THRESHOLD;
  digitalWrite(ledPin(WIND_LED_DEVICE_ID), warning ? HIGH : LOW);
  Serial.print("Wind ");
  Serial.print(wind);
  Serial.println(warning ? " km/h -> WARNING (LED WIND on)" : " km/h");
  return wind;
}

void publishSensorData(int wind) {
  float temperature = dht.readTemperature();
  float humidity = dht.readHumidity();
  float lux = bh1750Ready ? lightMeter.readLightLevel() : NAN;

  // Only include the fields that were read successfully.
  JsonDocument doc;
  if (!isnan(temperature)) doc["temperature"] = serialized(String(temperature, 1));
  if (!isnan(humidity)) doc["humidity"] = serialized(String(humidity, 1));
  if (!isnan(lux) && lux >= 0) doc["lux"] = serialized(String(lux, 1));
  doc["wind_speed"] = wind;

  if (doc.size() == 0) {
    Serial.println("No sensor could be read");
    return;
  }

  char buf[128];
  size_t n = serializeJson(doc, buf, sizeof(buf));
  client.publish(TOPIC_SENSOR, reinterpret_cast<const uint8_t*>(buf), n, false);

  Serial.print("PUB -> ");
  Serial.println(buf);
}

void setup() {
  Serial.begin(115200);

  for (size_t i = 0; i < LED_COUNT; i++) {
    pinMode(LED_PINS[i], OUTPUT);
  }
  for (size_t i = 0; i < LED_COUNT; i++) {
    digitalWrite(LED_PINS[i], LOW);
  }

  dht.begin();

  Wire.begin(SDA_PIN, SCL_PIN);
  bh1750Ready = lightMeter.begin(BH1750::CONTINUOUS_HIGH_RES_MODE);
  Serial.println(bh1750Ready ? "BH1750 ready" : "BH1750 init FAILED");

  WiFi.mode(WIFI_STA);
  WiFi.setAutoReconnect(true);

  client.setServer(MQTT_SERVER, MQTT_PORT);
  client.setCallback(mqttCallback);
}

void loop() {
  ensureWiFi();
  ensureMqtt();
  client.loop();

  unsigned long now = millis();
  if (now - lastSensorTime >= SENSOR_INTERVAL_MS) {
    lastSensorTime = now;
    int wind = updateWindWarning();
    if (client.connected()) publishSensorData(wind);
  }
}

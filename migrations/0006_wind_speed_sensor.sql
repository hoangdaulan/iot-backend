-- A fourth sensor: wind speed in km/h (simulated by the firmware, 0 to 500).
ALTER TABLE sensors DROP CONSTRAINT IF EXISTS sensors_type_check;
ALTER TABLE sensors ADD CONSTRAINT sensors_type_check
    CHECK (type IN ('temperature', 'humidity', 'light', 'wind_speed'));

INSERT INTO sensors (id, name, type, unit, mqtt_topic)
VALUES (4, 'Wind Speed', 'wind_speed', 'km/h', 'myiot/esp32/sensor-data')
ON CONFLICT (type) DO NOTHING;
SELECT setval(pg_get_serial_sequence('sensors', 'id'), (SELECT max(id) FROM sensors));

-- LED 3 is the wind warning light: it is driven by the wind speed, not by the user.
UPDATE devices SET name = 'LED WIND', type = 'Warning Led' WHERE id = 3;

-- The board has three LEDs, each its own device.
ALTER TABLE devices ADD COLUMN type TEXT NOT NULL DEFAULT 'LED';
ALTER TABLE devices ALTER COLUMN type DROP DEFAULT;

UPDATE devices SET name = 'LED 1' WHERE id = 1;
INSERT INTO devices (id, name, type, status)
VALUES (2, 'LED 2', 'LED', 'OFF'),
       (3, 'LED 3', 'LED', 'OFF');

SELECT setval(pg_get_serial_sequence('devices', 'id'), (SELECT max(id) FROM devices));

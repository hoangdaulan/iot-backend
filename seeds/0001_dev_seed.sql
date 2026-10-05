-- Development data. Idempotent: safe to run on every start (RUN_SEEDS=true).
-- Development credentials (password for both): 123456
--   admin  / admin@myiot.local  (ADMIN)
--   user01 / user01@myiot.local (USER)

CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- crypt(..., gen_salt('bf')) produces standard bcrypt ($2a$) hashes.
INSERT INTO users (name, email, password, username, phone, github, figma, role)
VALUES ('Administrator', 'admin@myiot.local', crypt('123456', gen_salt('bf', 10)), 'admin',
        '+84 912 345 678', 'https://github.com/myiot-admin', 'https://figma.com/@myiot-admin', 'ADMIN'),
       ('Nguyen Van A', 'user01@myiot.local', crypt('123456', gen_salt('bf', 10)), 'user01',
        NULL, NULL, NULL, 'USER')
ON CONFLICT DO NOTHING;

-- Seven days of readings every 10 minutes, only into an empty table.
INSERT INTO sensor_data (sensor_id, value, timestamp)
SELECT s.id,
       round(CASE s.type
                 WHEN 'temperature' THEN 26 + 5 * sin(radians(extract(hour FROM t) * 15 - 90)) + random() * 2 - 1
                 WHEN 'humidity' THEN 65 - 12 * sin(radians(extract(hour FROM t) * 15 - 90)) + random() * 4 - 2
                 ELSE greatest(0, 450 + 450 * sin(radians(extract(hour FROM t) * 15 - 90)) + random() * 40 - 20)
             END::numeric, 1)::double precision,
       t
FROM sensors s
CROSS JOIN generate_series(date_trunc('minute', now()) - interval '7 days',
                           date_trunc('minute', now()), interval '10 minutes') AS t
WHERE NOT EXISTS (SELECT 1 FROM sensor_data);

-- Forty LED commands over the same week, only into an empty table.
INSERT INTO device_actions (user_id, device_id, action, result, message, timestamp, completed_at)
SELECT u.id,
       1,
       CASE WHEN n % 2 = 0 THEN 'TURN_ON' ELSE 'TURN_OFF' END,
       CASE WHEN n % 10 = 3 THEN 'TIMEOUT' WHEN n % 7 = 5 THEN 'FAILED' ELSE 'SUCCESS' END,
       CASE
           WHEN n % 10 = 3 THEN 'Device did not respond in time'
           WHEN n % 7 = 5 THEN 'Device failed to turn ' || CASE WHEN n % 2 = 0 THEN 'on' ELSE 'off' END
           ELSE 'Device turned ' || CASE WHEN n % 2 = 0 THEN 'on' ELSE 'off' END || ' successfully'
       END,
       now() - (n * interval '4 hours'),
       now() - (n * interval '4 hours') + interval '1 second'
FROM generate_series(1, 40) AS n
JOIN users u ON u.username = CASE WHEN n % 3 = 0 THEN 'user01' ELSE 'admin' END
WHERE NOT EXISTS (SELECT 1 FROM device_actions);

-- Generates wind speed readings (0 to 500 km/h) for every day of the current month, one reading
-- every 10 minutes from the 1st until now (Vietnam time), so the Wind Report screen has data.
--
-- Each day has its own "windiness" factor, so the daily number of warnings (readings >= 300) differs
-- from day to day, and a few calm days have none. Readings that already exist are skipped, so the
-- script can be run again. It only inserts into sensor_data: it does not touch devices or users.
--
--   psql "$DATABASE_URL" -f scripts/wind_month_data.sql

INSERT INTO sensor_data (sensor_id, value, timestamp)
SELECT s.id,
       round(least(500, random() * 500 *
                        (0.45 + 0.9 * abs(sin(extract(day FROM t AT TIME ZONE 'Asia/Ho_Chi_Minh') * 1.7))))::numeric, 0)::double precision,
       t
FROM sensors s,
     generate_series(date_trunc('month', now() AT TIME ZONE 'Asia/Ho_Chi_Minh') AT TIME ZONE 'Asia/Ho_Chi_Minh',
                     now(), interval '10 minutes') AS t
WHERE s.type = 'wind_speed'
  AND NOT EXISTS (SELECT 1 FROM sensor_data d WHERE d.sensor_id = s.id AND d.timestamp = t);

-- Check: warnings (wind >= 300) per day of the current month.
SELECT to_char(d.timestamp AT TIME ZONE 'Asia/Ho_Chi_Minh', 'YYYY-MM-DD') AS day,
       count(*) FILTER (WHERE d.value >= 300) AS warnings,
       count(*) AS readings
FROM sensor_data d JOIN sensors s ON s.id = d.sensor_id
WHERE s.type = 'wind_speed'
  AND d.timestamp >= date_trunc('month', now() AT TIME ZONE 'Asia/Ho_Chi_Minh') AT TIME ZONE 'Asia/Ho_Chi_Minh'
GROUP BY day ORDER BY day;

-- To remove the generated data again (this month's wind readings):
-- DELETE FROM sensor_data
-- WHERE sensor_id = (SELECT id FROM sensors WHERE type = 'wind_speed')
--   AND timestamp >= date_trunc('month', now() AT TIME ZONE 'Asia/Ho_Chi_Minh') AT TIME ZONE 'Asia/Ho_Chi_Minh';

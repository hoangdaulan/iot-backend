-- Runtime settings edited from the app. For now: the wind speed (km/h) from which the warning
-- LED lights up and a reading counts as a warning.
CREATE TABLE settings (
    key        TEXT        PRIMARY KEY,
    value      TEXT        NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO settings (key, value) VALUES ('wind_warning_threshold', '300')
ON CONFLICT (key) DO NOTHING;

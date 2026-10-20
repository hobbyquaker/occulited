// occulited task 75: the KNX User Forum icons Control maps (scripts/knx-icons.mjs ships exactly these;
// lib/app/stateicons.test.ts checks the shipped set against the list), and the set's pinned commit.
export const COMMIT = '07a7016a4da1eb10db5546139cde11abf0ec6048'; // 2023-06-15, the set's last commit
export const NAMES = [
    'control_switch_1',
    ...[0, 10, 20, 30, 40, 50, 60, 70, 80, 90, 100].map((v) => `light_light_dim_${String(v).padStart(2, '0')}`),
    'light_led_stripe_rgb', 'message_socket', 'message_socket_on_off',
    ...[10, 20, 30, 40, 50, 60, 70, 80, 90, 100].map((v) => `fts_shutter_${v}`),
    'fts_door', 'fts_door_locked', 'fts_door_unlocked', 'fts_door_open', 'fts_door_opener_key',
    'fts_window_1w', 'fts_window_1w_tilt', 'fts_window_1w_open',
    'message_presence', 'message_presence_active',
    'secur_smoke_detector', 'secur_alarm', 'secur_alarm_alarm', 'sani_leakage',
    'temp_temperature', 'measure_power_meter',
    'weather_humidity_rel', 'weather_light_meter', 'weather_rain', 'weather_humidity_soil', 'weather_wind_speed', 'weather_station_generic',
];

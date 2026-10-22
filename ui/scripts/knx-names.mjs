// occulited task 75: the KNX User Forum icons Control maps (scripts/knx-icons.mjs ships exactly these;
// lib/app/stateicons.test.ts checks the shipped set against the list), and the set's pinned commit.
export const COMMIT = '07a7016a4da1eb10db5546139cde11abf0ec6048'; // 2023-06-15, the set's last commit
// occulited task 99: the icons a switch tile can be given in the assignment sheet ("Symbol"), in the picker's order;
// light_light_dim_00 and message_socket stand for their on/off pairs (light_light_dim_100, message_socket_on_off)
export const SWITCH_ICONS = [
    'light_light_dim_00', 'light_ceiling_light', 'light_pendant_light', 'light_pendant_light_round', 'light_wall_1', 'light_floor_lamp',
    'light_table_lamp', 'light_bedside', 'light_ceiling_spots', 'light_downlight', 'light_led_stripe', 'light_mirror', 'light_stairs',
    'light_outdoor', 'light_garage', 'light_fairy_lights', 'light_x-mas_tree', 'light_x-mas_star_1',
    'message_socket', 'vent_ventilation', 'sani_pump', 'sani_garden_pump', 'sani_pool_filter_pump', 'sani_heating', 'sani_towel_rad',
    'sani_irrigation', 'scene_coffee_maker_automatic', 'it_television', 'it_media_player', 'it_router', 'it_pc',
    'scene_washing_machine', 'scene_dishwasher',
];
// occulited task 106: the icons a key channel (a remote's or wall button's key, a virtual key, a push-button input) can be
// given; control_switch_1 is today's default
export const KEY_ICONS = [
    'control_switch_1', 'control_switch_3', 'control_switch_m_4', 'control_switch_m_8', 'control_on_off', 'control_all_on_off', 'control_standby',
    'control_home', 'control_ok', 'control_plus', 'control_minus', 'control_reload', 'control_reset', 'it_remote',
    'message_bell', 'message_bell_door', 'light_light_dim_00', 'scene_scene', 'scene_scene_teach_in',
    'scene_party', 'scene_day', 'scene_night', 'scene_sleeping', 'scene_cinema', 'scene_dinner', 'scene_visit_guests', 'scene_cleaning', 'scene_garden', 'scene_living',
];
// occulited task 105: the icons a door/window contact can be given per state (closed, open, tilted); the fts family
export const CONTACT_ICONS = [
    'fts_window_1w', 'fts_window_1w_open', 'fts_window_1w_tilt', 'fts_window_2w', 'fts_window_2w_open', 'fts_window_2w_tilt',
    'fts_window_roof', 'fts_window_roof_open_1', 'fts_window_louvre', 'fts_window_louvre_open', 'fts_light_dome', 'fts_light_dome_open',
    'fts_door', 'fts_door_open', 'fts_door_tilt', 'fts_frontdoor', 'fts_door_slide', 'fts_door_slide_open',
    'fts_garage', 'fts_garage_door_100', 'fts_sliding_gate_closed', 'fts_sliding_gate_open', 'fts_yard_gate_2w', 'fts_yard_gate_2w_open',
];
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
    ...SWITCH_ICONS.filter((n) => !['light_light_dim_00', 'message_socket'].includes(n)),
    ...KEY_ICONS.filter((n) => !['control_switch_1', 'light_light_dim_00'].includes(n)),
    ...CONTACT_ICONS.filter((n) => !['fts_window_1w', 'fts_window_1w_open', 'fts_window_1w_tilt', 'fts_door', 'fts_door_open'].includes(n)),
];

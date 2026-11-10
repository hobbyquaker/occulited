// occulited task 142: the check's report of what an uploaded backup is, for the specs that stub
// POST /restore/check - an (Open)CCU backup is imported, an openccu-lite backup restored.
export function report(kind: 'ccu' | 'openccu-lite', radio: object, more: object = {}) {
    return {
        kind,
        system: kind === 'ccu' ? 'OpenCCU' : 'openccu-lite',
        version: '3.89.11.20260919',
        hostname: 'old-ccu',
        key_index: (radio as {key_index?: number}).key_index ?? 0,
        radio,
        rega: kind === 'ccu',
        accounts: kind !== 'ccu',
        users: kind === 'ccu' ? 0 : 2,
        tokens: kind === 'ccu' ? 0 : 1,
        addons: [],
        settings: {},
        left_behind: kind === 'ccu' ? ['rega', 'users', 'network', 'other'] : [],
        ...more,
    };
}
export const THIS = {name: 'openccu-lite', version: '1.2.0', base_version: '3.89.11.20260919'};
export const EMPTY_RADIO = {bidcos_rf: {devices: 0, has_key: false, gateways: 0}, hmip: {devices: 0, local_key: false, device_key_map: false}, bidcos_wired: {devices: 0, gateways: 0}, key_index: 0, files: []};

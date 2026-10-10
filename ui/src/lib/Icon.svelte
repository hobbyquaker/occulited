<script lang="ts" module>
    /*
     * The line icons of the shell. Twelve 24×24 paths, drawn here rather than pulled from a set:
     * a CDN is not reachable from the box and an icon font would be a dependency and a second
     * visual language. They are stroke-only, `currentColor`, 2 px, round joins - the same weight
     * as the hairlines of the theme (D-21), so an icon sits beside a label without shouting.
     *
     * Nothing here carries meaning on its own: every icon in the UI stands next to the words it
     * illustrates, which is what makes it decoration a screen reader may skip (`aria-hidden`).
     */
    export type IconName =
        | 'server'
        | 'clock'
        | 'activity'
        | 'memory'
        | 'disk'
        | 'globe'
        | 'radio'
        | 'firmware'
        | 'download'
        | 'package'
        | 'alert'
        | 'gauge'
        | 'tag'
        | 'user'
        | 'settings'
        | 'lock'
        | 'help'
        | 'info'
        | 'network'
        | 'wifi'
        | 'power'
        | 'restart'
        // task 87: the Services page's row actions and their menus
        | 'play'
        | 'stop'
        | 'zap'
        | 'log'
        | 'more'
        | 'edit'
        | 'toggle-on'
        | 'toggle-off' | 'leave' | 'plus' | 'battery'
        | 'trash'
        | 'lifebuoy'
        | 'github'
        | 'licences'
        | 'x'
        // task 104: the text filter's magnifier (lib/SearchInput.svelte)
        | 'search'
        // task 81: silence a Status page warning - a bell struck through
        | 'bell-off'
        // task 59: pin an addon to the tab bar - a pushpin, its head filled by CSS when pinned
        | 'pin'
        // task 57: the System menu's entries - the firewall, the certificates, security, the users,
        // the backup, the status LED (the network, services, log and firmware icons exist above)
        | 'shield'
        | 'award'
        | 'key'
        | 'users'
        | 'archive'
        | 'bulb'
        | 'eye'
        | 'eye-off'
        // task 199: a capability a module has or has not - a tick and a cross in a circle
        | 'check-circle'
        | 'x-circle'
        // openccu-lite task 222: the LAN devices page - a box with two antennas and its lamps
        | 'router'
        // occulited task 27: the service messages' kinds - a broken link (communication
        // disturbed), a clock turned back (it was disturbed), an arrow up (an update pending)
        | 'unlink'
        | 'history'
        | 'arrow-up';

    const PATHS: Record<IconName, string> = {
        // the head is one closed shape so `fill: currentColor` on the svg fills it; the needle is a line
        pin: '<path d="M9.5 3h5l-.6 6 3.1 2.6V14H7v-2.4l3.1-2.6-.6-6z"/><path d="M12 14v7"/>',
        'bell-off': '<path d="M13.73 21a2 2 0 0 1-3.46 0"/><path d="M18.63 13A17.9 17.9 0 0 1 18 8"/><path d="M6.26 6.26A5.9 5.9 0 0 0 6 8c0 7-3 9-3 9h14"/><path d="M18 8a6 6 0 0 0-9.33-5"/><path d="M2 2l20 20"/>',
        // a box with two bays and a status lamp in each
        server: '<rect x="3" y="4" width="18" height="7" rx="2"/><rect x="3" y="13" width="18" height="7" rx="2"/><path d="M7 7.5h.01M7 16.5h.01"/>',
        clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3.5 2"/>',
        unlink: '<path d="M9 17H7a5 5 0 0 1 0-10h2"/><path d="M15 7h2a5 5 0 0 1 4 8"/><path d="M8 12h3"/><path d="M3 3l18 18"/>',
        history: '<path d="M3 12a9 9 0 1 0 3-6.7L3 8"/><path d="M3 3v5h5"/><path d="M12 7v5l3 2"/>',
        'arrow-up': '<circle cx="12" cy="12" r="9"/><path d="M12 16V8"/><path d="M8.5 11.5L12 8l3.5 3.5"/>',
        // the maintainer, 2026-09-22: a green tick before what a module can do, a red cross before
        // what it cannot (the HmIP routing line of the connections)
        'check-circle': '<circle cx="12" cy="12" r="9"/><path d="M8.2 12.4l2.6 2.6 5-5.4"/>',
        'x-circle': '<circle cx="12" cy="12" r="9"/><path d="M9.2 9.2l5.6 5.6M14.8 9.2l-5.6 5.6"/>',
        // the maintainer, 2026-09-20: a secret shown or hidden (SecretInput)
        eye: '<path d="M2 12s3.5-6 10-6 10 6 10 6-3.5 6-10 6-10-6-10-6z"/><circle cx="12" cy="12" r="3"/>',
        'eye-off': '<path d="M4.5 7.5C2.9 9 2 12 2 12s3.5 6 10 6c1.7 0 3.2-.4 4.4-1"/><path d="M9.9 5.2A9.9 9.9 0 0 1 12 5c6.5 0 10 6 10 6s-1 1.8-2.8 3.4"/><path d="M9.9 9.9a3 3 0 0 0 4.2 4.2"/><path d="M2 2l20 20"/>',
        // the ECG line of a load average: quiet, one spike, quiet
        activity: '<path d="M3 12h3.5L9 5.5l3.5 13L15 12h6"/>',
        // a chip: the die, the core, the legs
        memory: '<rect x="6" y="6" width="12" height="12" rx="1.5"/><rect x="9.5" y="9.5" width="5" height="5" rx="1"/><path d="M9.5 3v3M14.5 3v3M9.5 18v3M14.5 18v3M3 9.5h3M3 14.5h3M18 9.5h3M18 14.5h3"/>',
        // a stack of platters
        disk: '<ellipse cx="12" cy="6" rx="8" ry="3"/><path d="M4 6v12c0 1.66 3.58 3 8 3s8-1.34 8-3V6"/><path d="M4 12c0 1.66 3.58 3 8 3s8-1.34 8-3"/>',
        // a padlock: the certificate card
        lock: '<rect x="5" y="11" width="14" height="10" rx="2"/><path d="M8 11V7a4 4 0 0 1 8 0v4"/><path d="M12 15v2"/>',
        globe: '<circle cx="12" cy="12" r="9"/><path d="M3 12h18"/><ellipse cx="12" cy="12" rx="4" ry="9"/>',
        // an antenna radiating
        radio: '<circle cx="12" cy="12" r="2"/><path d="M8.2 15.8a5.4 5.4 0 0 1 0-7.6M15.8 8.2a5.4 5.4 0 0 1 0 7.6"/><path d="M5.4 18.6a9.4 9.4 0 0 1 0-13.2M18.6 5.4a9.4 9.4 0 0 1 0 13.2"/>',
        // a version dropping into a device
        firmware: '<path d="M12 3v10"/><path d="M8 9.5l4 4 4-4"/><path d="M4 16v3a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-3"/>',
        // the Log page's download: the same arrow into a tray, under the name of what it does there
        download: '<path d="M12 3v10"/><path d="M8 9.5l4 4 4-4"/><path d="M4 16v3a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-3"/>',
        package: '<path d="M12 3l8 4.5v9L12 21l-8-4.5v-9L12 3z"/><path d="M4 7.5l8 4.5 8-4.5M12 12v9"/>',
        alert: '<path d="M12 4l9 16H3l9-16z"/><path d="M12 10v4M12 17.2h.01"/>',
        // a dial with its needle
        gauge: '<path d="M4 18a9 9 0 1 1 16 0"/><path d="M12 18l4.5-5.5"/>',
        tag: '<path d="M11 3H4a1 1 0 0 0-1 1v7l9.5 9.5a1.5 1.5 0 0 0 2.1 0l6.9-6.9a1.5 1.5 0 0 0 0-2.1L11 3z"/><path d="M7.5 7.5h.01"/>',
        // task 29: the account, the settings, the source
        user: '<circle cx="12" cy="8" r="4"/><path d="M4 21a8 8 0 0 1 16 0"/>',
        // task 51: the round ? of lib/Help.svelte - a circle, the hook of the question mark, its dot
        // task 246: a bundle's info file - an i in a circle
        info: '<circle cx="12" cy="12" r="9"/><path d="M12 11v6"/><path d="M12 7.5h.01"/>',
        help: '<circle cx="12" cy="12" r="9"/><path d="M9.6 9.3a2.5 2.5 0 0 1 4.85.85c0 1.65-2.45 2.2-2.45 3.85"/><path d="M12 17.1h.01"/>',
        settings: '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"/>',
        // the Network page's interface panels: a wired link (a switch with two ports), Wi-Fi
        network: '<rect x="9" y="3" width="6" height="5" rx="1"/><rect x="3" y="16" width="6" height="5" rx="1"/><rect x="15" y="16" width="6" height="5" rx="1"/><path d="M12 8v4M6 16v-4h12v4"/>',
        router: '<rect x="3" y="13" width="18" height="7" rx="2"/><path d="M8 13l-2-8M16 13l2-8"/><path d="M7 16.5h.01M11 16.5h.01"/>',
        wifi: '<path d="M2.5 9a14 14 0 0 1 19 0"/><path d="M5.5 12.5a9.5 9.5 0 0 1 13 0"/><path d="M8.8 16a5 5 0 0 1 6.4 0"/><path d="M12 19.5h.01"/>',
        // the top bar's power menu: the power symbol, a reboot's circular arrow, the recovery's lifebuoy
        power: '<path d="M12 3v9"/><path d="M6.3 6.8a8 8 0 1 0 11.4 0"/>',
        restart: '<path d="M20 12a8 8 0 1 1-2.34-5.66"/><path d="M20 4v5h-5"/>',
        // task 87: the Services page. Start a triangle, Stop a rounded square; Run now a flash - a
        // timer's service once, at once - so it does not read as Start in the column under it
        play: '<path d="M7 4.8v14.4a1 1 0 0 0 1.53.85l11.5-7.2a1 1 0 0 0 0-1.7L8.53 3.95A1 1 0 0 0 7 4.8z"/>',
        stop: '<rect x="5" y="5" width="14" height="14" rx="2.5"/>',
        zap: '<path d="M13 2.5L4.5 13.5h7l-1 8 8.5-11h-7l1-8z"/>',
        // a page with a folded corner and lines of text
        log: '<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8l-5-5z"/><path d="M14 3v5h5"/><path d="M9 13h6M9 17h6"/>',
        // three dots, filled below: stroked they vanish at 14 px
        more: '<circle cx="5" cy="12" r="2"/><circle cx="12" cy="12" r="2"/><circle cx="19" cy="12" r="2"/>',
        // a pencil over its line
        edit: '<path d="M12 20h8"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4 12.5-12.5z"/>',
        // a switch: the knob right is on, left is off
        'toggle-on': '<rect x="2" y="6" width="20" height="12" rx="6"/><circle cx="16" cy="12" r="2.5"/>',
        'toggle-off': '<rect x="2" y="6" width="20" height="12" rx="6"/><circle cx="8" cy="12" r="2.5"/>',
        // task 193: the way out of the App when it is the whole window - an arrow leaving the frame
        leave: '<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><path d="M16 17l5-5-5-5"/><path d="M21 12H9"/>',
        plus: '<path d="M12 5v14M5 12h14"/>',
        battery: '<rect x="2" y="7" width="17" height="10" rx="2"/><path d="M22 10v4"/><path d="M5 10v4"/>',
        trash: '<path d="M4 7h16"/><path d="M6.5 7l.9 12.1a2 2 0 0 0 2 1.9h5.2a2 2 0 0 0 2-1.9L17.5 7"/><path d="M9.5 7V4.5h5V7"/><path d="M10.5 11v6M13.5 11v6"/>',
        lifebuoy: '<circle cx="12" cy="12" r="9"/><circle cx="12" cy="12" r="4"/><path d="M5.6 5.6l3.6 3.6M14.8 14.8l3.6 3.6M18.4 5.6l-3.6 3.6M9.2 14.8l-3.6 3.6"/>',
        // a cross: remove the row it ends (the Interfaces page's subscriptions)
        x: '<path d="M6 6l12 12M18 6L6 18"/>',
        // task 104: a magnifier - the lens and its handle
        search: '<circle cx="11" cy="11" r="6.5"/><path d="M20 20l-4.4-4.4"/>',
        // task 57, the System menu: a shield for the firewall
        shield: '<path d="M12 3l7 3v5.5c0 4.4-3 8.3-7 9.5-4-1.2-7-5.1-7-9.5V6z"/>',
        // a rosette with its ribbons: the certificates
        award: '<circle cx="12" cy="9" r="5"/><path d="M9.2 13.4L8 21l4-2 4 2-1.2-7.6"/>',
        // a key: security - the login, the tokens
        key: '<circle cx="7.5" cy="15.5" r="4.5"/><path d="M10.7 12.3L21 2"/><path d="M15 8l3 3"/>',
        // two people: the users
        users: '<circle cx="9" cy="8" r="3.5"/><path d="M3 20c0-3.3 2.7-6 6-6s6 2.7 6 6"/><path d="M15.5 4.8a3.5 3.5 0 0 1 0 6.4"/><path d="M17.5 14.3c2.1.8 3.5 2.9 3.5 5.7"/>',
        // a box with a lid: the backup
        archive: '<rect x="3" y="4" width="18" height="4" rx="1"/><path d="M5 8v10a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8"/><path d="M10 12h4"/>',
        // a bulb: the status LED
        bulb: '<path d="M9 18h6"/><path d="M10 21h4"/><path d="M8.5 14.5A6 6 0 1 1 15.5 14.5c-.6.6-1 1.5-1 2.5h-5c0-1-.4-1.9-1-2.5z"/>',
        // the GitHub mark is a filled shape, not a line; drawn filled below
        github: '<path d="M12 2C6.48 2 2 6.58 2 12.25c0 4.53 2.87 8.37 6.84 9.73.5.1.68-.22.68-.49 0-.24-.01-.88-.01-1.73-2.78.62-3.37-1.37-3.37-1.37-.45-1.18-1.11-1.5-1.11-1.5-.91-.64.07-.62.07-.62 1 .07 1.53 1.06 1.53 1.06.89 1.57 2.34 1.11 2.91.85.09-.66.35-1.11.63-1.37-2.22-.26-4.56-1.14-4.56-5.06 0-1.12.39-2.03 1.03-2.75-.1-.26-.45-1.3.1-2.71 0 0 .84-.28 2.75 1.05A9.3 9.3 0 0 1 12 6.96c.85 0 1.7.12 2.5.35 1.91-1.33 2.75-1.05 2.75-1.05.55 1.41.2 2.45.1 2.71.64.72 1.03 1.63 1.03 2.75 0 3.93-2.34 4.8-4.57 5.05.36.32.68.94.68 1.9 0 1.37-.01 2.48-.01 2.82 0 .27.18.59.69.49A10.25 10.25 0 0 0 22 12.25C22 6.58 17.52 2 12 2z"/>',
        // task 179: the Licences page - the GitHub mark's circle (centre 12/12.25, radius 10) with a §
        // cut out of it (DejaVu Sans Bold's glyph, 13 high), even-odd so its loops stay filled
        licences: '<path fill-rule="evenodd" d="M2 12.25a10 10 0 1 0 20 0a10 10 0 1 0-20 0zM14.80 6.19L14.80 6.19L14.80 7.91Q14.05 7.61 13.46 7.47Q12.86 7.32 12.42 7.32L12.42 7.32Q11.86 7.32 11.57 7.50Q11.28 7.69 11.28 8.05L11.28 8.05Q11.28 8.55 12.70 9.15L12.70 9.15Q12.90 9.24 13.00 9.27L13.00 9.27Q14.59 9.95 15.20 10.61Q15.80 11.26 15.80 12.21L15.80 12.21Q15.80 13.10 15.37 13.70Q14.94 14.30 14.05 14.66L14.05 14.66Q14.64 14.98 14.92 15.40Q15.21 15.83 15.21 16.38L15.21 16.38Q15.21 17.48 14.29 18.12Q13.37 18.75 11.76 18.75L11.76 18.75Q11.11 18.75 10.43 18.64Q9.74 18.53 8.97 18.31L8.97 18.31L8.97 16.51Q9.84 16.82 10.53 16.99Q11.22 17.15 11.65 17.15L11.65 17.15Q12.14 17.15 12.44 16.96Q12.74 16.77 12.74 16.47L12.74 16.47Q12.74 15.94 11.37 15.38L11.37 15.38Q11.10 15.27 10.95 15.21L10.95 15.21Q9.42 14.55 8.81 13.87Q8.20 13.19 8.20 12.21L8.20 12.21Q8.20 11.42 8.62 10.84Q9.04 10.25 9.90 9.87L9.90 9.87Q9.33 9.48 9.09 9.06Q8.84 8.64 8.84 8.07L8.84 8.07Q8.84 6.98 9.71 6.36Q10.57 5.75 12.10 5.75L12.10 5.75Q12.74 5.75 13.42 5.86Q14.10 5.97 14.80 6.19ZM11.39 10.78L11.39 10.78Q10.87 11.00 10.62 11.28Q10.36 11.57 10.36 11.95L10.36 11.95Q10.36 12.46 10.83 12.83Q11.30 13.20 12.67 13.70L12.67 13.70Q13.17 13.53 13.43 13.23Q13.70 12.93 13.70 12.53L13.70 12.53Q13.70 12.03 13.17 11.62Q12.65 11.22 11.39 10.78Z"/>',
    };
    const FILLED = new Set<IconName>(['github', 'more', 'licences']);
</script>

<script lang="ts">
    interface Props {
        name: IconName;
        /** px; the icons are drawn on a 24 grid and scale from there */
        size?: number;
        /** stroke width in the 24-grid, thinned automatically for the small sizes */
        width?: number;
    }
    let {name, size = 16, width}: Props = $props();
    const stroke = $derived(width ?? (size <= 14 ? 2.2 : 2));
</script>

<svg
    class="ol-icon"
    viewBox="0 0 24 24"
    width={size}
    height={size}
    fill={FILLED.has(name) ? 'currentColor' : 'none'}
    stroke={FILLED.has(name) ? 'none' : 'currentColor'}
    stroke-width={stroke}
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
    focusable="false">
    <!-- eslint-disable-next-line svelte/no-at-html-tags -- the table above is the only source -->
    {@html PATHS[name]}
</svg>

<style>
    .ol-icon {
        flex: 0 0 auto;
        display: block;
    }
</style>

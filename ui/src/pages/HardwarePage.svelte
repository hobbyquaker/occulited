<script lang="ts">
    /*
     * openccu-lite task 340 (the maintainer, 2026-10-15): System → Hardware, the former Status LED
     * page with a section per part of the hardware, each shown only where the system has it: the
     * status LED (LEDSection, unchanged), the radio module's button (ButtonSection, task 164) and the
     * HDMI output, and the Pi 4/5 bootloader (BootloaderSection, task 366). WLAN and Bluetooth come later.
     *
     * HDMI is off unless switched on. The system switches it off at runtime once the boot has
     * finished, not in config.txt, so a monitor still shows the boot and the recovery system; on
     * gives the output back at once (PUT /api/system/v1/hardware/hdmi).
     */
    import {onMount} from 'svelte';
    import {pageLife} from '../lib/pagelife.svelte';
    import {api} from '../lib/api';
    import {t} from '../lib/i18n.svelte';
    import SystemTitle from '../lib/SystemTitle.svelte';
    import {auth} from '../lib/auth.svelte';
    import Help from '../lib/Help.svelte';
    import LEDSection from './LEDSection.svelte';
    import ButtonSection from './ButtonSection.svelte';
    import BootloaderSection from './BootloaderSection.svelte';

    interface HDMIView {
        available: boolean;
        reason?: string;
        enabled: boolean;
        connectors: {name: string; status: string; enabled: boolean; dpms: string}[];
        pending?: boolean;
        error?: string;
    }

    let hdmi = $state<HDMIView | null>(null);
    let hdmiError = $state('');
    let busy = $state(false);
    const admin = $derived(auth.role === 'admin');

    const life = pageLife();
    onMount(() => {
        void loadHDMI();
        const stopReturn = life.onReturn(() => void loadHDMI());
        return stopReturn;
    });

    async function loadHDMI() {
        try {
            hdmi = await api.get<HDMIView>('/api/system/v1/hardware/hdmi');
        } catch {
            hdmi = null;
        }
    }

    async function setHDMI(e: Event) {
        const box = e.currentTarget as HTMLInputElement;
        busy = true;
        hdmiError = '';
        try {
            hdmi = await api.put<HDMIView>('/api/system/v1/hardware/hdmi', {enabled: box.checked});
        } catch (err) {
            hdmiError = (err as Error).message;
            box.checked = !box.checked;
            void loadHDMI();
        } finally {
            busy = false;
        }
    }

    /** a connector's state in a few words: what a monitor on it gets */
    function connectorText(c: HDMIView['connectors'][number]): string {
        if (c.enabled && c.dpms === 'On') return t('output active');
        if (c.enabled) return t('output idle (screen blanked)');
        return t('no output');
    }
</script>

<SystemTitle><Help>{t('The hardware parts of the system that can be switched or configured. A section shows only where the system has that part.')}</Help></SystemTitle>

<LEDSection />

<!-- openccu-lite task 164 -->
<ButtonSection />

{#if hdmi?.available}
    <!-- task 58: a heading and a panel, as the LED sections -->
    <h2 id="hdmi">{t('HDMI')}<Help>{t('The HDMI output of the Raspberry Pi. Off, the system switches it off once it has started: a monitor still shows the boot and the recovery system. On, the output is back at once; the console still blanks the screen after two minutes without input.')}</Help></h2>
    <section class="ol-card ol-hw-panel" data-panel="hdmi">
        {#if hdmiError}<div class="ol-notice error">{hdmiError}</div>{/if}
        {#if hdmi.error}<div class="ol-notice error" data-hdmi-error>{t('The HDMI output could not be switched:')} {hdmi.error}</div>{/if}
        <label class="ol-hw-check">
            <input type="checkbox" checked={hdmi.enabled} onchange={setHDMI} disabled={!admin || busy} data-hdmi="enabled" />
            {t('HDMI output on')}
        </label>
        <p class="ol-muted" data-hdmi-state>
            {#if hdmi.enabled}{t('The output is on.')}{:else if hdmi.pending}{t('The output is switched off once the system has finished starting.')}{:else}{t('The output is off. A monitor shows the boot and the recovery system.')}{/if}
        </p>
        {#if hdmi.connectors.length}
            <ul class="ol-hw-list" data-hdmi-connectors>
                {#each hdmi.connectors as c (c.name)}
                    <li><span class="hmm-mono">{c.name}</span> <span class="ol-muted">{connectorText(c)}</span></li>
                {/each}
            </ul>
        {/if}
    </section>
{/if}

<!-- openccu-lite task 366: the Pi 4/5 bootloader EEPROM, offered, never flashed by itself -->
<BootloaderSection />

<style>
    .ol-hw-panel { display: flex; flex-direction: column; align-items: flex-start; gap: 8px; }
    .ol-hw-panel > p { margin: 0; }
    .ol-hw-check { display: inline-flex; align-items: center; gap: 6px; }
    .ol-hw-list { list-style: none; margin: 6px 0 0; padding: 0; display: flex; flex-direction: column; gap: 4px; }
</style>

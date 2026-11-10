<script module lang="ts">
    export interface PortVerdict { port: number; proto: string; target: string; rule?: string; source?: string; owner?: string; sources?: string[] }
</script>

<script lang="ts">
    /*
     * What the firewall does with the ports a feature needs, in one line per feature: each port with
     * its target (iptables' word, untranslated) - "(Policy)" when no rule names it, else from where:
     * every source an accepted port is accepted from, joined in one line (B-134: a firewall converted
     * from OpenCCU has a rule per allowed address, and the first alone made the access look narrower
     * than it is), the deciding rule's source for a port that is not accepted - a one-line hint
     * the API chose, and the button to the Firewall page, where the rules are changed. The `error`
     * look when a port that is needed is not accepted.
     *
     * openccu-lite task 217 built it for the HmIP access points (their `ap-firewall`); task 223 (the
     * maintainer, 2026-09-24: "remove the firewall table. i want the same panel as we have for the
     * firewall rules for hmip-access-points instead, same style") gives classic RPC the same, so it
     * is one component. The look is that of `.ol-notice`, under a class of its own: a page's other
     * notices are found by that class.
     */
    import type {HTMLAttributes} from 'svelte/elements';
    import {link} from './router.svelte';
    import {t} from './i18n.svelte';

    interface Props extends HTMLAttributes<HTMLDivElement> {
        ports: PortVerdict[];
        /** the hint, translated */
        text: string;
        error?: boolean;
    }
    let {ports, text, error = false, ...rest}: Props = $props();
    // the firewall's own token for the local networks stays as iptables' page says it; 0/0 is anywhere
    const sourceText = (s: string) => (s === '0/0' ? 'anywhere' : s);
    // an older daemon answers without `sources`: the deciding rule's is what there is then
    const from = (p: PortVerdict) => (p.target === 'ACCEPT' && p.sources?.length ? p.sources : p.source ? [p.source] : []).map(sourceText).join(', ');
</script>

<div class="ol-fw-verdicts" class:error {...rest}>
    <span class="fw-text">
        {#if ports.length}
            <span class="fw-ports">
                {#each ports as p (p.proto + p.port)}
                    <span class="fw-port" data-port={p.port}><span class="hmm-mono">{p.proto} {p.port}</span> <span class="ol-badge" class:good={p.target === 'ACCEPT'} class:bad={p.target !== 'ACCEPT'}>{p.target}</span>{#if !p.rule}<span class="ol-muted">{" "}({t('Policy')})</span>{:else if from(p)}<span class="ol-muted fw-source">{" "}{t('from {source}', {source: from(p)})}</span>{/if}</span>
                {/each}
            </span>
        {/if}
        {text}
    </span>
    <a class="hmm-button" href="/system/firewall" use:link>{t('Firewall')}</a>
</div>

<style>
    .ol-fw-verdicts { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 14px; margin: 10px 0; padding: 8px 10px; border-radius: var(--hmm-radius); border: 1px solid var(--hmm-border); background: var(--hmm-bg-sunken); }
    .ol-fw-verdicts.error { border-color: var(--hmm-error); }
    .fw-text { flex: 1 1 260px; min-width: 0; }
    .fw-ports { display: flex; flex-wrap: wrap; gap: 4px 14px; margin-bottom: 4px; }
    /* the port and its target stay one piece; a long list of sources wraps behind them */
    .fw-port > .hmm-mono, .fw-port > .ol-badge { white-space: nowrap; }
    .fw-source { overflow-wrap: anywhere; }
    .fw-port .ol-badge { margin-left: 2px; }
    .ol-fw-verdicts > a.hmm-button { text-decoration: none; color: var(--hmm-fg); white-space: nowrap; }
</style>

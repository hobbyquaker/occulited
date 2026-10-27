/*
 * occulited task 118 (D-122): the update channels. The daemon stores the channel
 * (system_update.channel) and treats anything but beta or dev as stable; so does this module. Their
 * names are UpdateChannel.svelte's (channelLabel, channelName), which has the translations.
 */
export type Channel = 'stable' | 'beta' | 'dev';

export const CHANNELS: readonly Channel[] = ['stable', 'beta', 'dev'];

/** The channel a stored or answered value stands for: stable unless it names beta or dev. */
export function normalChannel(c: unknown): Channel {
    return c === 'beta' || c === 'dev' ? c : 'stable';
}

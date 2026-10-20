// occulited B-90: a backup is prepared on the system before its download. GET /backup alone writes
// nothing until the .sbk is complete - minutes on a large /usr/local - and lighttpd ends an HTTP/2
// stream that writes nothing for 6 minutes. So the page starts the backup (POST
// /backup/prepare), polls it, and then follows /backup?prepared=<id>, which streams at once.
import {api} from './api';

export interface BackupJob {
    id: string;
    state: 'running' | 'ready' | 'downloaded' | 'failed';
    started: string;
    finished?: string;
    size?: number;
    error?: string;
}

export const BACKUP_URL = '/api/system/v1/backup';

const wait = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

/**
 * Starts the backup, or joins the one that runs or waits, and resolves when it is ready to
 * download; a failed one throws its reason. onState sees every answer.
 */
export async function prepareBackup(onState?: (job: BackupJob) => void, interval = 2000, sleep: (ms: number) => Promise<void> = wait): Promise<BackupJob> {
    let job = await api.post<BackupJob>(`${BACKUP_URL}/prepare`);
    onState?.(job);
    while (job.state === 'running') {
        await sleep(interval);
        job = await api.get<BackupJob>(`${BACKUP_URL}/prepare/${encodeURIComponent(job.id)}`);
        onState?.(job);
    }
    if (job.state !== 'ready') throw new Error(job.error || job.state);
    return job;
}

/** The download URL of a prepared backup, with further query parameters (encrypted=false, confirm). */
export function preparedUrl(id: string, extra: Record<string, string> = {}): string {
    const q = new URLSearchParams({prepared: id, ...extra});
    return `${BACKUP_URL}?${q.toString()}`;
}

/** Seconds as m:ss - the time the backup has been running. */
export function elapsed(seconds: number): string {
    const s = Math.max(0, Math.floor(seconds));
    return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
}

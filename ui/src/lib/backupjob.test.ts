import {afterEach, describe, expect, it, vi} from 'vitest';
import {elapsed, prepareBackup, preparedUrl, type BackupJob} from './backupjob';
import {api} from './api';

afterEach(() => vi.restoreAllMocks());

describe('prepareBackup (B-90)', () => {
    it('starts, polls until ready, and reports every state', async () => {
        const states: string[] = [];
        vi.spyOn(api, 'post').mockResolvedValue({id: 'j1', state: 'running', started: ''} as BackupJob);
        const get = vi.spyOn(api, 'get').mockResolvedValueOnce({id: 'j1', state: 'running', started: ''} as BackupJob).mockResolvedValueOnce({id: 'j1', state: 'ready', started: '', size: 9} as BackupJob);
        const job = await prepareBackup((j) => states.push(j.state), 1, () => Promise.resolve());
        expect(job.size).toBe(9);
        expect(states).toEqual(['running', 'running', 'ready']);
        expect(get).toHaveBeenCalledWith('/api/system/v1/backup/prepare/j1');
    });
    it('joins a ready one without polling', async () => {
        vi.spyOn(api, 'post').mockResolvedValue({id: 'j2', state: 'ready', started: ''} as BackupJob);
        const get = vi.spyOn(api, 'get');
        expect((await prepareBackup(undefined, 1, () => Promise.resolve())).id).toBe('j2');
        expect(get).not.toHaveBeenCalled();
    });
    it('throws the reason of a failed one', async () => {
        vi.spyOn(api, 'post').mockResolvedValue({id: 'j3', state: 'running', started: ''} as BackupJob);
        vi.spyOn(api, 'get').mockResolvedValue({id: 'j3', state: 'failed', started: '', error: 'createBackup.sh: exit status 1'} as BackupJob);
        await expect(prepareBackup(undefined, 1, () => Promise.resolve())).rejects.toThrow('createBackup.sh: exit status 1');
    });
});

describe('preparedUrl and elapsed', () => {
    it('builds the download URL', () => {
        expect(preparedUrl('ab')).toBe('/api/system/v1/backup?prepared=ab');
        expect(preparedUrl('ab', {encrypted: 'false', confirm: 'c 1'})).toBe('/api/system/v1/backup?prepared=ab&encrypted=false&confirm=c+1');
    });
    it('formats m:ss', () => {
        expect(elapsed(0)).toBe('0:00');
        expect(elapsed(75.9)).toBe('1:15');
        expect(elapsed(-3)).toBe('0:00');
    });
});

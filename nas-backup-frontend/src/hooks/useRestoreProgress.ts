import { useState, useEffect, useCallback, useRef } from 'react';
import { createRestoreProgressStream, RestoreProgressEvent } from '@/utils/api';

export interface RestoreProgressState {
  isRunning: boolean;
  jobId: number | null;
  phase: string;
  phaseName: string;
  message: string;
  current: number;
  total: number;
  percent: number;
  filePath: string;
  restoredSize: number;
  totalSize: number;
  logs: Array<{ level: string; message: string; timestamp: string }>;
}

export function useRestoreProgress() {
  const [state, setState] = useState<RestoreProgressState>({
    isRunning: false,
    jobId: null,
    phase: '',
    phaseName: '',
    message: '',
    current: 0,
    total: 0,
    percent: 0,
    filePath: '',
    restoredSize: 0,
    totalSize: 0,
    logs: [],
  });

  const cleanupRef = useRef<(() => void) | null>(null);

  const disconnect = useCallback(() => {
    if (cleanupRef.current) {
      cleanupRef.current();
      cleanupRef.current = null;
    }
  }, []);

  const connect = useCallback(() => {
    disconnect();

    cleanupRef.current = createRestoreProgressStream(
      (event: RestoreProgressEvent) => {
        switch (event.type) {
          case 'connected':
            setState(s => ({ ...s }));
            break;
          case 'phase':
            setState(s => ({
              ...s,
              isRunning: !['completed', 'failed', 'cancelled'].includes(event.phase || ''),
              jobId: event.job_id || s.jobId,
              phase: event.phase || '',
              phaseName: event.phase_name || '',
              message: event.message || '',
            }));
            if (['completed', 'failed', 'cancelled'].includes(event.phase || '')) {
              disconnect();
            }
            break;
          case 'progress':
            setState(s => ({
              ...s,
              current: event.current ?? s.current,
              total: event.total ?? s.total,
              percent: event.percent ?? s.percent,
              restoredSize: event.restored_size ?? s.restoredSize,
              totalSize: event.total_size ?? s.totalSize,
              message: `恢复中... ${event.current}/${event.total} 个文件`,
            }));
            break;
          case 'file':
            setState(s => ({
              ...s,
              filePath: event.file_path || '',
            }));
            break;
          case 'log':
            setState(s => ({
              ...s,
              logs: [...s.logs.slice(-99), {
                level: event.level || 'info',
                message: event.message || '',
                timestamp: event.timestamp,
              }],
            }));
            break;
        }
      },
      () => {
        console.error('Restore SSE connection error');
      }
    );
  }, [disconnect]);

  useEffect(() => {
    return () => disconnect();
  }, [disconnect]);

  const reset = useCallback(() => {
    disconnect();
    setState({
      isRunning: false,
      jobId: null,
      phase: '',
      phaseName: '',
      message: '',
      current: 0,
      total: 0,
      percent: 0,
      filePath: '',
      restoredSize: 0,
      totalSize: 0,
      logs: [],
    });
  }, [disconnect]);

  return { ...state, connect, disconnect, reset };
}

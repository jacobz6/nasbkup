import { useState, useCallback } from 'react';
import type { APIResponse } from '@/utils/api';

export function useApi<T>() {
  const [data, setData] = useState<T | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const execute = useCallback(async (apiCall: () => Promise<APIResponse<T>>) => {
    setLoading(true);
    setError(null);
    const result = await apiCall();
    if (result.success && result.data !== undefined) {
      setData(result.data);
      return result.data;
    } else {
      const errMsg = result.error || '请求失败';
      setError(errMsg);
      throw new Error(errMsg);
    }
  }, []);

  const reset = useCallback(() => {
    setData(null);
    setLoading(false);
    setError(null);
  }, []);

  return { data, loading, error, execute, reset, setData };
}

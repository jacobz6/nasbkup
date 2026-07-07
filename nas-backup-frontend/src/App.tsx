import { BrowserRouter, Routes, Route } from 'react-router-dom';
import { AppLayout } from '@/components/layout/AppLayout';
import { Dashboard } from '@/pages/Dashboard';
import { Content } from '@/pages/Content';
import { Strategy } from '@/pages/Strategy';
import { Logs } from '@/pages/Logs';
import { Reconcile } from '@/pages/Reconcile';
import { Restore } from '@/pages/Restore';

function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route element={<AppLayout />}>
          <Route path="/" element={<Dashboard />} />
          <Route path="/content" element={<Content />} />
          <Route path="/strategy" element={<Strategy />} />
          <Route path="/logs" element={<Logs />} />
          <Route path="/reconcile" element={<Reconcile />} />
          <Route path="/restore" element={<Restore />} />
        </Route>
      </Routes>
    </BrowserRouter>
  );
}

export default App;

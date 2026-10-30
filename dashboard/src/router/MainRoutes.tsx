import { Navigate, useRoutes, type Location } from 'react-router-dom';
import { DashboardPage } from '@/features/dashboard/DashboardPage';
import { ProvidersPage } from '@/features/providers/ProvidersPage';
import { ClientKeysPage } from '@/features/clientKeys/ClientKeysPage';
import { AuthFilesPage } from '@/features/authFiles/AuthFilesPage';
import { OAuthPage } from '@/features/oauth/OAuthPage';
import { SystemPage } from '@/pages/SystemPage';

const mainRoutes = [
  { path: '/', element: <DashboardPage /> },
  { path: '/dashboard', element: <DashboardPage /> },
  { path: '/client-keys', element: <ClientKeysPage /> },
  { path: '/ai-providers', element: <ProvidersPage /> },
  { path: '/auth-files', element: <AuthFilesPage /> },
  { path: '/oauth', element: <OAuthPage /> },
  { path: '/system', element: <SystemPage /> },
  { path: '*', element: <Navigate to="/" replace /> },
];

export function MainRoutes({ location }: { location?: Location }) {
  return useRoutes(mainRoutes, location);
}

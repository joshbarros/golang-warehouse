export interface Sku {
  id: string;
  code: string;
  name: string;
  category: string;
  bin: string;
  receivedAt: string;
  receivedUnits: number;
  onHandUnits: number;
}

export type OrderStatus = 'Requested' | 'Held' | 'Dispatched' | 'Cancelled' | 'Rejected';

export interface Order {
  id: string;
  skuCode: string;
  ownerId: string;
  reference: string;
  units: number;
  feeCents: number;
  status: OrderStatus;
  reason: string | null;
  slipNumber: string | null;
  createdAt: string;
  updatedAt: string;
  unitsReleasedAt: string | null;
}

export interface LogEntry {
  id: string;
  type: string;
  module: string;
  occurredAt: string;
}

export interface UserInfo {
  id: string;
  email: string;
  roles: string[];
}

export interface AuthResponse {
  accessToken: string;
  expiresAt: string;
  user: UserInfo;
}

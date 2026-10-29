import { HttpClient } from '@angular/common/http';
import { Injectable, inject } from '@angular/core';
import { firstValueFrom } from 'rxjs';
import { Order, LogEntry, Sku } from './models';

export interface NewSku {
  code: string;
  name: string;
  category: string;
  bin: string;
  receivedUnits: number;
}

@Injectable({ providedIn: 'root' })
export class ApiService {
  private readonly http = inject(HttpClient);

  skus = () => firstValueFrom(this.http.get<Sku[]>('/api/skus'));
  openSku = (body: NewSku) => firstValueFrom(this.http.post<Sku>('/api/skus', body));

  orders = (skuCode: string) =>
    firstValueFrom(this.http.get<Order[]>('/api/orders', { params: { skuCode } }));
  book = (body: { skuCode: string; reference: string; units: number }) =>
    firstValueFrom(this.http.post<Order>('/api/orders', body));
  cancel = (id: string) =>
    firstValueFrom(this.http.post<Order>(`/api/orders/${id}/cancellation`, {}));
  events = (id: string) => firstValueFrom(this.http.get<LogEntry[]>(`/api/orders/${id}/events`));
}

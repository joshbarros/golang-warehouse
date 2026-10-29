import { test, expect } from '@playwright/test';
import { ADMIN, adminToken, signIn } from './support';

test('add a SKU, place an order, see the pick slip, cancel it', async ({ page }) => {
  const run = Date.now().toString(36).toUpperCase();
  const code = `E2E-${run}`;
  const reference = `ORD-${run}`;

  await signIn(page, ADMIN);
  await page.getByLabel('SKU code').fill(code);
  await page.getByLabel('Units received').fill('8');
  await page.getByRole('button', { name: 'Add SKU' }).click();

  const remaining = page.getByTestId('selected-remaining');
  await expect(page.getByRole('heading', { name: `Place an order for ${code}` })).toBeVisible();
  await expect(remaining).toHaveText('8');

  await page.getByLabel('Order reference').fill(reference);
  await page.getByLabel('Units', { exact: true }).fill('3');
  await page.getByRole('button', { name: 'Place order' }).click();

  const row = page.locator(`tr[data-reference="${reference}"]`);
  await expect(row.getByTestId('status')).toHaveText('Dispatched', { timeout: 20_000 });
  await expect(row.getByTestId('slip')).toHaveText(/PS-[0-9A-F]{12}/);
  await expect(remaining).toHaveText('5');

  // Nothing is kept in the browser: a fresh load reads the same state from the API.
  // Nothing sensitive is stored either: the refresh cookie is HttpOnly, so a reload signs back in through it.
  expect(await page.evaluate(() => JSON.stringify({ ...localStorage, ...sessionStorage }))).toBe(
    '{}',
  );
  await page.reload();
  await expect(page.getByTestId('whoami')).toContainText(ADMIN.email);
  await page.getByRole('button', { name: new RegExp(`^${code}`) }).click();
  const reloaded = page.locator(`tr[data-reference="${reference}"]`);
  await expect(reloaded.getByTestId('status')).toHaveText('Dispatched');
  await expect(page.getByTestId('selected-remaining')).toHaveText('5');

  await reloaded.getByRole('button', { name: 'Cancel' }).click();
  await expect(reloaded.getByTestId('status')).toHaveText('Cancelled');
  await expect(page.getByTestId('selected-remaining')).toHaveText('8');
  // The issued slip is not rolled back.
  await expect(reloaded.getByTestId('slip')).toHaveText(/PS-[0-9A-F]{12}/);
  const headers = { Authorization: `Bearer ${await adminToken(page.request)}` };
  const orders = await page.request.get(`/api/orders?skuCode=${code}`, { headers });
  const [order] = await orders.json();
  const slip = await page.request.get(`/api/slips/${order.id}`, { headers });
  expect((await slip.json()).status).toBe('Issued');
});

import { test, expect } from '@playwright/test';
import { ADMIN, CUSTOMER_PASSWORD, adminToken, registerCustomer, signIn } from './support';

const run = () => Date.now().toString(36).toUpperCase() + Math.floor(Math.random() * 1000);

test('an anonymous visitor sees only the sign-in form', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'SKUs' })).toHaveCount(0);
});

test('a wrong password is refused with a message, and nothing opens', async ({ page }) => {
  await page.goto('/');
  await page.getByLabel('Email').fill(ADMIN.email);
  await page.getByLabel('Password').fill('Wrong-Passw0rd');
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page.getByRole('alert')).toContainText(/invalid|incorrect|unauthor/i);
  await expect(page.getByTestId('whoami')).toHaveCount(0);
});

test('registration enforces the password policy and explains it', async ({ page }) => {
  await page.goto('/');
  await page.getByRole('button', { name: 'Need an account? Register' }).click();
  await page.getByLabel('Email').fill(`weak-${run()}@example.com`);
  await page.getByLabel('Password').fill('short');
  await page.getByRole('button', { name: 'Create account' }).click();
  await expect(page.getByRole('alert')).toBeVisible();
  await expect(page.getByTestId('whoami')).toHaveCount(0);
});

test('a customer books, sees only their own orders, and has no staff controls', async ({
  page,
  browser,
}) => {
  const id = run();
  const SKU = `AUTH-${id}`;
  // Staff open the SKU through the API; the customer never sees the form.
  const baseURL = test.info().project.use.baseURL;
  const staff = await browser.newContext({ baseURL });
  const token = await adminToken(staff.request);
  const opened = await staff.request.post('/api/skus', {
    headers: { Authorization: `Bearer ${token}` },
    data: {
      code: SKU,
      name: 'Blue Auth',
      category: 'Hardware',
      bin: 'A-01',
      receivedUnits: 6,
    },
  });
  expect(opened.status()).toBe(201);

  await registerCustomer(page);
  await expect(page.getByRole('heading', { name: 'Add a SKU' })).toHaveCount(0);
  await page.getByRole('button', { name: new RegExp(`^${SKU}`) }).click();

  await page.getByLabel('Order reference').fill(`ORD-${id}`);
  await page.getByLabel('Units', { exact: true }).fill('1');
  await page.getByRole('button', { name: 'Place order' }).click();
  const mine = page.locator(`tr[data-reference="ORD-${id}"]`);
  await expect(mine.getByTestId('status')).toHaveText('Dispatched', { timeout: 20_000 });
  await expect(mine.getByRole('button', { name: 'Event log' })).toHaveCount(0);

  // Staff see the customer's order; a second customer does not.
  const otherContext = await browser.newContext({ baseURL });
  const other = await otherContext.newPage();
  await registerCustomer(other);
  await other.getByRole('button', { name: new RegExp(`^${SKU}`) }).click();
  await expect(other.locator(`tr[data-reference="ORD-${id}"]`)).toHaveCount(0);

  // A fresh browser: the API context above already holds the administrator's refresh cookie.
  const staffUi = await browser.newContext({ baseURL });
  const staffPage = await staffUi.newPage();
  await signIn(staffPage, ADMIN);
  await staffPage.getByRole('button', { name: new RegExp(`^${SKU}`) }).click();
  await expect(staffPage.locator(`tr[data-reference="ORD-${id}"]`)).toHaveCount(1);
  await expect(staffPage.getByRole('heading', { name: 'Add a SKU' })).toBeVisible();
  await staffUi.close();
  await staff.close();
  await otherContext.close();
});

test('signing out ends the session, and a reload stays signed out', async ({ page }) => {
  await registerCustomer(page);
  await page.getByRole('button', { name: 'Sign out' }).click();
  await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible();
  await page.reload();
  await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible();
  await expect(page.getByTestId('whoami')).toHaveCount(0);
});

test('the session survives a reload through the refresh cookie', async ({ page }) => {
  const email = await registerCustomer(page);
  await page.reload();
  await expect(page.getByTestId('whoami')).toContainText(email);
  // The cookie is invisible to scripts: only the access token in memory is usable from the page.
  expect(await page.evaluate(() => document.cookie)).toBe('');
  expect(CUSTOMER_PASSWORD.length).toBeGreaterThan(9);
});

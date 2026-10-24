// SPDX-License-Identifier: AGPL-3.0-only
let csrf;
let events;
const status = document.querySelector('#status');
const enqueue = document.querySelector('#enqueue');
const logout = document.querySelector('#logout');

async function request(path, body) {
  const response = await fetch(path, {
    method: 'POST', headers: {'Content-Type': 'application/json', 'X-CSRF-Token': csrf ?? ''},
    body: JSON.stringify(body),
  });
  if (!response.ok) throw new Error(`Request failed (${response.status})`);
  return response;
}

document.querySelector('#login').addEventListener('submit', async (event) => {
  event.preventDefault();
  try {
    const input = document.querySelector('#token');
    const response = await request('/api/login', {token: input.value});
    input.value = '';
    csrf = (await response.json()).csrf;
    enqueue.disabled = logout.disabled = false;
    events?.close();
    events = new EventSource('/api/events');
    events.addEventListener('state', (event) => { status.textContent = event.data; });
    events.onerror = () => { status.textContent = 'Reconnecting…'; };
  } catch (error) { status.textContent = error.message; }
});

enqueue.addEventListener('click', async () => {
  try {
    const bytes = crypto.getRandomValues(new Uint8Array(32));
    const id = Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
    await request('/api/operations', {id, delay_seconds: 0});
  } catch (error) { status.textContent = error.message; }
});

logout.addEventListener('click', async () => {
  try {
    await request('/api/logout', {});
    events?.close();
    csrf = undefined;
    enqueue.disabled = logout.disabled = true;
    status.textContent = 'Signed out.';
  } catch (error) { status.textContent = error.message; }
});

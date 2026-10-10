"use client";

import { useCallback, useEffect, useState } from "react";

const statuses = ["pending", "failed", "pushed"];

export default function SyncPage() {
  const [vouchers, setVouchers] = useState([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(null);

  const load = useCallback(async () => {
    try {
      const res = await fetch("/backend/vouchers", { cache: "no-store" });
      if (!res.ok) {
        setError("Could not load vouchers");
        return;
      }
      const data = await res.json();
      setVouchers(data.vouchers || []);
      setError("");
    } catch {
      setError("API is not reachable. Start the Go API on port 8080.");
    }
  }, []);

  useEffect(() => {
    load();
    const id = setInterval(load, 2000);
    return () => clearInterval(id);
  }, [load]);

  async function retry(id) {
    setBusy(id);
    try {
      await fetch(`/backend/vouchers/${id}/retry`, { method: "POST" });
      await load();
    } finally {
      setBusy(null);
    }
  }

  return (
    <main>
      <h1>Sutra sync</h1>
      <p className="note">Vouchers waiting to reach accounting, refreshed every 2 seconds.</p>
      {error ? <p className="error">{error}</p> : null}
      {statuses.map((status) => {
        const rows = vouchers.filter((item) => item.status === status);
        return (
          <section key={status}>
            <h2>{status}</h2>
            {rows.length === 0 ? <p>None</p> : null}
            {rows.map((item) => (
              <article key={item.id}>
                <div>
                  Voucher {item.id} · {item.attempts} attempts · ₹{(item.amount_paise / 100).toFixed(2)}
                </div>
                {item.last_error ? <div>Reason: {item.last_error}</div> : null}
                {item.external_ref ? <div>Accounting ref: {item.external_ref}</div> : null}
                {status === "failed" ? (
                  <button type="button" disabled={busy === item.id} onClick={() => retry(item.id)}>
                    Retry
                  </button>
                ) : null}
              </article>
            ))}
          </section>
        );
      })}
    </main>
  );
}

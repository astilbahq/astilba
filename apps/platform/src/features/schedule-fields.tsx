import { useId, useState } from "react";
import type { Schema } from "../api/client";
import { Input } from "../components/input";
import { Label } from "../components/label";
import { Select } from "../components/select";
import * as s from "../styles";
import { Field, text } from "../ui";

type Schedule = Schema["DailySchedule"];

export function scheduleInput(data: FormData): Schedule {
  const kind = text(data, "scheduleKind") as NonNullable<Schedule["kind"]>;
  const [hour, minute] =
    kind === "daily" ? text(data, "time").split(":").map(Number) : [0, 0];
  return {
    hour,
    minute,
    timezone: kind === "interval" ? "UTC" : text(data, "timezone"),
    ...(kind === "cron"
      ? { kind, expression: text(data, "expression") }
      : kind === "interval"
        ? { kind, intervalMinutes: Number(text(data, "intervalMinutes")) }
        : {}),
  };
}

export function scheduleTimeout(data: FormData) {
  const seconds = Number(text(data, "timeoutSeconds"));
  return seconds === 1800 ? undefined : seconds;
}

export function scheduleDescription(schedule: Schedule) {
  if (schedule.kind === "interval")
    return `Every ${schedule.intervalMinutes} elapsed minutes, anchored in UTC`;
  if (schedule.kind === "cron")
    return `Cron ${schedule.expression} (${schedule.timezone})`;
  return `Daily ${String(schedule.hour).padStart(2, "0")}:${String(schedule.minute).padStart(2, "0")} (${schedule.timezone || "UTC"})`;
}

export function nextRunDescription(next: string, schedule: Schedule) {
  const date = new Date(next);
  const timezone = schedule.timezone || "UTC";
  try {
    return date.toLocaleString(undefined, {
      timeZone: timezone,
      timeZoneName: "short",
    });
  } catch (error) {
    if (!(error instanceof RangeError)) throw error;
    // Go's embedded tzdata includes zones absent from some browser Intl sets.
    return `${date.toLocaleString(undefined, { timeZone: "UTC", timeZoneName: "short" })} (UTC display; ${timezone} is unavailable in this browser)`;
  }
}

export function ScheduleFields({
  initial,
  timeout = 1800,
}: {
  initial?: Schedule;
  timeout?: number;
}) {
  const [kind, setKind] = useState(initial?.kind ?? "daily");
  const hint = useId();
  return (
    <fieldset className={s.stack}>
      <legend>Schedule</legend>
      <Label>
        Schedule type
        <Select
          name="scheduleKind"
          value={kind}
          onChange={(event) =>
            setKind(event.target.value as NonNullable<Schedule["kind"]>)
          }
        >
          <option value="daily">Daily</option>
          <option value="cron">Cron expression</option>
          <option value="interval">Fixed interval</option>
        </Select>
      </Label>
      {kind === "daily" && (
        <Label>
          Daily time
          <Input
            name="time"
            type="time"
            defaultValue={
              initial
                ? `${String(initial.hour).padStart(2, "0")}:${String(initial.minute).padStart(2, "0")}`
                : undefined
            }
            required
          />
        </Label>
      )}
      {kind === "cron" && (
        <>
          <Label>
            Cron expression
            <Input
              name="expression"
              defaultValue={initial?.expression ?? "0 2 * * *"}
              maxLength={128}
              aria-describedby={hint}
              required
            />
          </Label>
          <p id={hint}>
            Five fields: minute, hour, day of month, month, weekday (0–6,
            Sunday–Saturday). For example, 0 2 * * * runs at 02:00. Seconds and
            @ shortcuts are unavailable. When both day fields are restricted,
            either may match.
          </p>
        </>
      )}
      {kind === "interval" ? (
        <>
          <Label>
            Interval in minutes
            <Input
              name="intervalMinutes"
              type="number"
              min={1}
              max={525600}
              defaultValue={initial?.intervalMinutes ?? 60}
              required
            />
          </Label>
          <p>
            The first run is one interval after installation. Later runs stay
            anchored to that UTC instant, including across daylight-saving
            changes. Installing a new generation resets the anchor.
          </p>
        </>
      ) : (
        <>
          <Label>
            Timezone
            <Input
              name="timezone"
              defaultValue={initial?.timezone || "UTC"}
              maxLength={100}
              required
            />
          </Label>
          <p>
            Use an IANA timezone, such as Europe/London. Nonexistent local times
            are skipped; repeated wall-clock minutes run only on their first
            occurrence.
          </p>
        </>
      )}
      <Field
        name="timeoutSeconds"
        label="Run timeout in seconds"
        type="number"
        min={1}
        max={1800}
        defaultValue={timeout}
      />
      <p>
        Uploads and jobs stop after this limit, up to 30 minutes. Job cleanup
        can take another 30 seconds; an unknown outcome blocks further host
        operations until inspected. Fresh restores keep their separate 30-minute
        limit.
      </p>
    </fieldset>
  );
}

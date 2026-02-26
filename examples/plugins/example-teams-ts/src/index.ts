/**
 * Example Microsoft Teams notification plugin for RoboDev.
 *
 * Implements the NotificationChannel gRPC interface to send notifications
 * to Microsoft Teams channels via incoming webhooks.
 *
 * Note: This is an example/template. The @robodev/plugin-sdk package is
 * not yet published; this code shows the intended plugin development
 * experience.
 */

const TEAMS_WEBHOOK_URL = process.env.TEAMS_WEBHOOK_URL ?? '';

interface Ticket {
  id: string;
  title: string;
  description?: string;
  externalUrl: string;
}

interface TaskResult {
  success: boolean;
  mergeRequestUrl?: string;
  summary: string;
}

/**
 * TeamsNotificationChannel sends fire-and-forget notifications to
 * Microsoft Teams via incoming webhook.
 */
class TeamsNotificationChannel {
  private webhookUrl: string;

  constructor(webhookUrl: string) {
    this.webhookUrl = webhookUrl;
  }

  get name(): string {
    return 'teams';
  }

  get interfaceVersion(): number {
    return 1;
  }

  /**
   * Send a free-form notification message.
   */
  async notify(message: string, ticket: Ticket): Promise<void> {
    await this.sendCard({
      title: `RoboDev: ${ticket.title}`,
      text: message,
      ticketUrl: ticket.externalUrl,
    });
  }

  /**
   * Notify that an agent has started working on a ticket.
   */
  async notifyStart(ticket: Ticket): Promise<void> {
    await this.sendCard({
      title: `🤖 Agent started: ${ticket.title}`,
      text: `RoboDev agent has begun working on ticket ${ticket.id}.`,
      ticketUrl: ticket.externalUrl,
      themeColor: '0078D4', // Blue
    });
  }

  /**
   * Notify that an agent has completed working on a ticket.
   */
  async notifyComplete(ticket: Ticket, result: TaskResult): Promise<void> {
    const colour = result.success ? '00CC6A' : 'FF4444';
    const status = result.success ? 'succeeded' : 'failed';

    await this.sendCard({
      title: `${result.success ? '✅' : '❌'} Agent ${status}: ${ticket.title}`,
      text: result.summary + (result.mergeRequestUrl
        ? `\n\n[View Merge Request](${result.mergeRequestUrl})`
        : ''),
      ticketUrl: ticket.externalUrl,
      themeColor: colour,
    });
  }

  /**
   * Send an Adaptive Card to the Teams channel via webhook.
   */
  private async sendCard(opts: {
    title: string;
    text: string;
    ticketUrl?: string;
    themeColor?: string;
  }): Promise<void> {
    const card = {
      '@type': 'MessageCard',
      '@context': 'https://schema.org/extensions',
      themeColor: opts.themeColor ?? '0078D4',
      summary: opts.title,
      sections: [
        {
          activityTitle: opts.title,
          text: opts.text,
        },
      ],
      potentialAction: opts.ticketUrl
        ? [
            {
              '@type': 'OpenUri',
              name: 'View Ticket',
              targets: [{ os: 'default', uri: opts.ticketUrl }],
            },
          ]
        : [],
    };

    const response = await fetch(this.webhookUrl, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(card),
    });

    if (!response.ok) {
      console.error(`teams webhook failed: ${response.status} ${response.statusText}`);
    }
  }
}

/**
 * Entry point for the Teams plugin.
 *
 * In production, this would use the @robodev/plugin-sdk to register
 * the channel as a gRPC service and start serving.
 */
async function main(): Promise<void> {
  console.log('starting robodev-plugin-teams');

  if (!TEAMS_WEBHOOK_URL) {
    console.error('TEAMS_WEBHOOK_URL environment variable is required');
    process.exit(1);
  }

  const channel = new TeamsNotificationChannel(TEAMS_WEBHOOK_URL);
  console.log(`teams channel initialised: ${channel.name} v${channel.interfaceVersion}`);

  // When the SDK is available:
  // import { serve } from '@robodev/plugin-sdk';
  // serve(channel, { interface: 'notifications' });
}

main().catch((err) => {
  console.error('fatal error:', err);
  process.exit(1);
});

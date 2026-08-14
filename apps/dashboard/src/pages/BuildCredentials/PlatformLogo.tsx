import apple from '@/assets/apple.svg';
import android from '@/assets/android.svg';
import { cn } from '@/lib/utils';
import { Platform } from './platforms';

const logos: Record<Platform, { src: string; alt: string }> = {
  android: { src: android, alt: 'Android' },
  ios: { src: apple, alt: 'iOS' },
};

export const PlatformLogo = ({
  platform,
  className,
}: {
  platform: Platform;
  className?: string;
}) => (
  <img
    src={logos[platform].src}
    alt={logos[platform].alt}
    className={cn('object-contain brightness-0 opacity-80 dark:invert', className)}
  />
);

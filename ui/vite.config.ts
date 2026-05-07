import tailwindcss from '@tailwindcss/vite';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [tailwindcss(), sveltekit()],
	server: {
		port: 5180,
		strictPort: false,
		proxy: {
			'/ws': { target: 'ws://localhost:8080', ws: true, changeOrigin: true },
			'/api': 'http://localhost:8080',
			'/healthz': 'http://localhost:8080'
		}
	}
});
